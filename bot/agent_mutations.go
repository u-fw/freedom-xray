package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The self-updater and collector installer operate on os.Executable(). An
// agent launched from a temporary copy must not replace that copy and then
// restart the installed service, or write a collector unit pointing at it.
func requireInstalledAgent() error {
	current, err := os.Executable()
	if err != nil {
		return err
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return err
	}
	installed, err := filepath.EvalSymlinks("/usr/local/bin/freedom-bot")
	if err != nil {
		return errors.New("installed freedom-bot executable not found")
	}
	if current != installed {
		return errors.New("invoke /usr/local/bin/freedom-bot for Bot updates and collector installation")
	}
	return nil
}

type agentRoutingInput struct {
	RuleTag  string          `json:"rule_tag,omitempty"`
	Rule     json.RawMessage `json:"rule,omitempty"`
	Position int             `json:"position,omitempty"`
}

type agentRoutingRuleInput struct {
	Index     int             `json:"index"`
	ETag      string          `json:"etag"`
	Field     string          `json:"field,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Direction string          `json:"direction,omitempty"`
	ListETag  string          `json:"list_etag,omitempty"`
}

func agentRoutingRuleEdit(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentRoutingRuleInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	change := routingRuleEdit{Ref: routingRuleRef{Index: input.Index, ETag: input.ETag}}
	switch operation {
	case "routing.rule.patch", "routing.rule.target":
		if input.Field == "" || len(input.Value) == 0 || input.Direction != "" || input.ListETag != "" {
			return nil, errors.New("field and value are required")
		}
		value, err := decodeRoutingEditValue(input.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid field value: %w", err)
		}
		change.Field, change.Value = input.Field, value
		if operation == "routing.rule.patch" {
			change.Kind = "field"
		} else {
			change.Kind = "target"
		}
	case "routing.rule.move":
		if input.Field != "" || len(input.Value) > 0 || input.ListETag == "" {
			return nil, errors.New("move requires index, etag, list_etag and direction")
		}
		change.Kind = "move"
		change.ExpectedListETag = input.ListETag
		switch input.Direction {
		case "up":
			change.Direction = -1
		case "down":
			change.Direction = 1
		default:
			return nil, errors.New("direction must be up or down")
		}
	case "routing.rule.delete":
		if input.Field != "" || len(input.Value) > 0 || input.Direction != "" || input.ListETag != "" {
			return nil, errors.New("delete accepts only index and etag")
		}
		change.Kind = "delete"
	default:
		return nil, errors.New("unknown routing rule operation")
	}
	if !apply {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		message, err := applyRoutingRuleEdit(root, change)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, message), nil
	}
	return agentMessage(changeRoutingRule(cfg, change))
}

type agentNetworkInput struct {
	Tag      string          `json:"tag,omitempty"`
	Outbound json.RawMessage `json:"outbound,omitempty"`
	DNS      json.RawMessage `json:"dns,omitempty"`
}

type agentBalancerInput struct {
	Tag      string          `json:"tag,omitempty"`
	ETag     string          `json:"etag,omitempty"`
	Field    string          `json:"field,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`
	Balancer json.RawMessage `json:"balancer,omitempty"`
}

func agentBalancerChange(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentBalancerInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	change := balancerChange{Tag: input.Tag, ETag: input.ETag}
	switch operation {
	case "balancers.put":
		if input.Field != "" || len(input.Value) > 0 {
			return nil, errors.New("put accepts balancer, optional tag and etag")
		}
		item, err := decodeNetworkObject(input.Balancer)
		if err != nil {
			return nil, err
		}
		change.Kind, change.Balancer = "put", item
		if change.Tag == "" {
			change.Tag, _ = item["tag"].(string)
		}
	case "balancers.patch":
		if input.Tag == "" || input.ETag == "" || input.Field == "" || len(input.Value) == 0 || len(input.Balancer) > 0 {
			return nil, errors.New("patch requires tag, etag, field and value")
		}
		value, err := decodeRoutingEditValue(input.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid field value: %w", err)
		}
		change.Kind, change.Field, change.Value = "patch", input.Field, value
	case "balancers.delete":
		if input.Tag == "" || input.ETag == "" || input.Field != "" || len(input.Value) > 0 || len(input.Balancer) > 0 {
			return nil, errors.New("delete requires tag and etag")
		}
		change.Kind = "delete"
	default:
		return nil, errors.New("unknown balancer operation")
	}
	if !apply {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		message, err := applyBalancerChange(root, change)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, message), nil
	}
	return agentMessage(changeBalancerConfig(cfg, change))
}

type agentObservatoryInput struct {
	Kind      string          `json:"kind"`
	ETag      string          `json:"etag,omitempty"`
	Config    json.RawMessage `json:"config,omitempty"`
	Selectors json.RawMessage `json:"selectors,omitempty"`
	Field     string          `json:"field,omitempty"`
	Value     *string         `json:"value,omitempty"`
}

func agentObservatoryChange(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentObservatoryInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	change := observatoryChange{Kind: input.Kind, ETag: input.ETag}
	switch operation {
	case "observatories.put":
		if len(input.Selectors) > 0 || input.Field != "" || input.Value != nil {
			return nil, errors.New("put accepts kind, optional etag and config")
		}
		config, err := decodeNetworkObject(input.Config)
		if err != nil {
			return nil, err
		}
		change.Action, change.Config = "put", config
	case "observatories.selectors":
		if input.ETag == "" || len(input.Selectors) == 0 || len(input.Config) > 0 || input.Field != "" || input.Value != nil {
			return nil, errors.New("selectors requires kind, etag and selectors")
		}
		selectors, err := decodeRoutingEditValue(input.Selectors)
		if err != nil {
			return nil, fmt.Errorf("invalid selectors: %w", err)
		}
		change.Action, change.Selectors = "selectors", selectors
	case "observatories.patch":
		if input.ETag == "" || input.Field == "" || input.Value == nil || len(input.Config) > 0 || len(input.Selectors) > 0 {
			return nil, errors.New("patch requires kind, etag, field and value")
		}
		change.Action, change.Field, change.Value = "field", input.Field, *input.Value
	case "observatories.delete":
		if input.ETag == "" || len(input.Config) > 0 || len(input.Selectors) > 0 || input.Field != "" || input.Value != nil {
			return nil, errors.New("delete requires kind and etag")
		}
		change.Action = "delete"
	default:
		return nil, errors.New("unknown observatory operation")
	}
	if !apply {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		message, err := applyObservatoryChange(root, change)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, message), nil
	}
	return agentMessage(changeObservatoryConfig(cfg, change))
}

func agentNetworkChange(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentNetworkInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	change := networkChange{Kind: operation, Tag: input.Tag}
	switch operation {
	case "outbounds.delete":
		if len(input.Outbound) > 0 || len(input.DNS) > 0 {
			return nil, errors.New("outbounds.delete accepts only tag")
		}
	case "outbounds.put":
		if len(input.DNS) > 0 {
			return nil, errors.New("dns is not valid for this operation")
		}
		value, err := decodeNetworkObject(input.Outbound)
		if err != nil {
			return nil, err
		}
		change.Value = value
		tag, _ := value["tag"].(string)
		if change.Tag == "" {
			change.Tag = tag
		}
	case "dns.put":
		if input.Tag != "" || len(input.Outbound) > 0 {
			return nil, errors.New("dns.put accepts only dns")
		}
		value, err := decodeNetworkObject(input.DNS)
		if err != nil {
			return nil, err
		}
		change.Value = value
	default:
		return nil, errors.New("unknown network operation")
	}
	if !apply {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		message, err := applyNetworkChange(root, change)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, message), nil
	}
	return agentMessage(changeNetworkConfig(cfg, change))
}

func agentRoutingChange(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentRoutingInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	change := routingChange{Kind: strings.TrimPrefix(operation, "routing."), RuleTag: input.RuleTag, Position: input.Position}
	if change.Kind != "add" && input.Position != 0 {
		return nil, errors.New("position is only supported when adding a rule")
	}
	if change.Kind == "delete" {
		if len(input.Rule) > 0 {
			return nil, errors.New("routing.delete accepts only rule_tag")
		}
	} else {
		if len(input.Rule) == 0 {
			return nil, errors.New("rule is required")
		}
		rule, err := decodeRoutingRule(input.Rule)
		if err != nil {
			return nil, err
		}
		change.Rule = rule
		tag, _ := rule["ruleTag"].(string)
		if change.RuleTag == "" {
			change.RuleTag = tag
		}
		if tag != change.RuleTag {
			return nil, errors.New("rule_tag and rule.ruleTag must match")
		}
	}
	if !apply {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		message, err := applyRoutingChange(root, change)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, message), nil
	}
	return agentMessage(changeRoutingConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, change))
}

type agentNodeCreateInput struct {
	Template           NodeTemplate `json:"template"`
	Tag                string       `json:"tag"`
	Port               int          `json:"port"`
	SNI                string       `json:"sni"`
	Path               string       `json:"path,omitempty"`
	Mode               string       `json:"mode,omitempty"`
	XHTTPHost          string       `json:"xhttp_host,omitempty"`
	XHTTPPadding       string       `json:"xhttp_padding,omitempty"`
	PrivateKey         string       `json:"private_key,omitempty"`
	ShortID            string       `json:"short_id,omitempty"`
	Decryption         string       `json:"decryption,omitempty"`
	ClientEncryption   string       `json:"client_encryption,omitempty"`
	MLDSA65Seed        string       `json:"mldsa65_seed,omitempty"`
	GenerateMLDSA      bool         `json:"generate_mldsa,omitempty"`
	GenerateEncryption bool         `json:"generate_encryption,omitempty"`
	Vision             *bool        `json:"vision,omitempty"`
	CertificateSource  string       `json:"certificate_source,omitempty"`
	CertFile           string       `json:"cert_file,omitempty"`
	KeyFile            string       `json:"key_file,omitempty"`
	ALPN               []string     `json:"alpn,omitempty"`
}

func agentCreateNode(cfg *BotConfig, raw []byte, apply bool) (any, error) {
	var input agentNodeCreateInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	if !validNodeTemplate(input.Template) {
		return nil, errors.New("unknown node template")
	}
	if input.GenerateEncryption && (input.Decryption != "" || input.ClientEncryption != "") {
		return nil, errors.New("generated encryption cannot be combined with supplied encryption values")
	}
	if input.GenerateMLDSA && input.MLDSA65Seed != "" {
		return nil, errors.New("generated ML-DSA cannot be combined with a supplied seed")
	}
	if input.Template == NodeTemplateXHTTPTLS && (input.GenerateMLDSA || input.MLDSA65Seed != "" || input.PrivateKey != "" || input.ShortID != "") {
		return nil, errors.New("REALITY keys and ML-DSA apply only to REALITY templates")
	}
	if input.Template == NodeTemplateTCPReality && (input.Path != "" || input.Mode != "" || input.XHTTPHost != "" || input.XHTTPPadding != "") {
		return nil, errors.New("XHTTP settings apply only to XHTTP templates")
	}
	if input.Template != NodeTemplateXHTTPTLS && len(input.ALPN) > 0 {
		return nil, errors.New("ALPN applies only to XHTTP/TLS")
	}
	if input.PrivateKey != "" {
		if err := validateRealityPrivateKey(input.PrivateKey); err != nil {
			return nil, err
		}
	}
	vision := input.Template == NodeTemplateTCPReality
	if input.Vision != nil {
		vision = *input.Vision
	}
	params := NodeTemplateParams{Tag: input.Tag, Port: input.Port, SNI: input.SNI, Path: input.Path, Mode: input.Mode,
		XHTTPHost: input.XHTTPHost, XHTTPPadding: input.XHTTPPadding, PrivateKey: input.PrivateKey, ShortID: input.ShortID,
		Decryption: input.Decryption, MLDSA65Seed: input.MLDSA65Seed, Vision: vision, CertFile: input.CertFile, KeyFile: input.KeyFile, ALPN: input.ALPN}
	if params.SNI == "" && input.Template != NodeTemplateXHTTPTLS {
		params.SNI = defaultRealitySNI
	}
	if input.Template == NodeTemplateXHTTPTLS {
		if input.CertificateSource == "" {
			input.CertificateSource = "existing"
		}
		if input.CertificateSource != "existing" && input.CertificateSource != "self-signed" {
			return nil, errors.New("certificate_source must be existing or self-signed")
		}
		if input.CertificateSource == "self-signed" {
			if params.CertFile != "" || params.KeyFile != "" {
				return nil, errors.New("self-signed certificates use managed paths; do not supply paths")
			}
			params.CertFile = filepath.Join(certsDirectory(cfg.XrayConfigPath), params.SNI+".crt")
			params.KeyFile = filepath.Join(certsDirectory(cfg.XrayConfigPath), params.SNI+".key")
		}
	} else if input.CertificateSource != "" || params.CertFile != "" || params.KeyFile != "" {
		return nil, errors.New("certificate settings apply only to XHTTP/TLS")
	}
	if params.Decryption != "" && params.Decryption != "none" && input.ClientEncryption == "" {
		return nil, errors.New("client_encryption is required with custom decryption")
	}
	if input.ClientEncryption != "" && (params.Decryption == "" || params.Decryption == "none") {
		return nil, errors.New("decryption is required with client_encryption")
	}
	if input.ClientEncryption != "" {
		if err := validateNodeSecret(input.ClientEncryption); err != nil {
			return nil, err
		}
	}
	if params.Decryption != "" && params.Decryption != "none" {
		if err := validateNodeSecret(params.Decryption); err != nil {
			return nil, err
		}
	}
	if params.MLDSA65Seed != "" {
		if err := validateNodeSecret(params.MLDSA65Seed); err != nil {
			return nil, err
		}
	}
	// Preview validates deterministic inputs without generating credentials.
	preview := params
	if preview.PrivateKey == "" {
		preview.PrivateKey = "preview-only"
	}
	if preview.ShortID == "" {
		preview.ShortID = "0000000000000000"
	}
	if preview.Path == "" {
		preview.Path = "/preview"
	}
	if err := ValidateNodeTemplateParams(input.Template, preview); err != nil {
		return nil, err
	}
	if !apply {
		return agentPreview("nodes.create", map[string]any{"template": input.Template, "tag": params.Tag, "port": params.Port, "sni": params.SNI, "certificate_source": input.CertificateSource}), nil
	}
	if params.ShortID == "" && input.Template != NodeTemplateXHTTPTLS {
		var err error
		params.ShortID, err = randomShortID()
		if err != nil {
			return nil, err
		}
	}
	if params.Path == "" && (input.Template == NodeTemplateXHTTPReality || input.Template == NodeTemplateXHTTPTLS) {
		var err error
		params.Path, err = randomXHTTPPath()
		if err != nil {
			return nil, err
		}
	}
	if input.GenerateMLDSA {
		var err error
		params.MLDSA65Seed, err = generateMLDSA65Seed()
		if err != nil {
			return nil, err
		}
	}
	if input.GenerateEncryption {
		var err error
		params.Decryption, input.ClientEncryption, err = generateVLESSEncryptionPair()
		if err != nil {
			return nil, err
		}
	}
	request := NodeCreateRequest{Template: input.Template, Params: params, ClientEncryption: input.ClientEncryption}
	if input.Template == NodeTemplateXHTTPTLS && input.CertificateSource == "self-signed" {
		var err error
		request.CertPEM, request.KeyPEM, err = GenerateSelfSignedCertificatePEM(params.SNI)
		if err != nil {
			return nil, err
		}
	}
	return agentMessage(CreateNode(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, request))
}

type agentNodeChangeInput struct {
	Tag          string `json:"tag"`
	Kind         string `json:"kind"`
	Value        string `json:"value,omitempty"`
	Secret       string `json:"secret,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

func agentUpdateNode(cfg *BotConfig, raw []byte, apply bool) (any, error) {
	var input agentNodeChangeInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	if err := validateNodeTag(input.Tag); err != nil {
		return nil, err
	}
	allowed := map[string]bool{"reality_sni": true, "reality_short_id": true, "path": true, "mode": true, "port": true, "listen": true,
		"xhttp_host": true, "xhttp_padding": true, "reality_dest": true, "reality_target": true, "reality_private_key": true,
		"reality_mldsa_seed": true, "decryption": true, "tls_pair": true, "alpn": true}
	if !allowed[input.Kind] {
		return nil, errors.New("unsupported node setting")
	}
	switch input.Kind {
	case "reality_private_key", "reality_mldsa_seed":
		if input.Value != "" || input.ClientSecret != "" {
			return nil, errors.New("this setting accepts only secret")
		}
	case "decryption":
		if input.Value != "" || ((input.Secret == "" || input.Secret == "none") && input.ClientSecret != "") {
			return nil, errors.New("invalid decryption and client_secret combination")
		}
	case "tls_pair":
		if input.ClientSecret != "" {
			return nil, errors.New("tls_pair accepts value and secret only")
		}
	default:
		if input.Secret != "" || input.ClientSecret != "" {
			return nil, errors.New("this setting accepts only value")
		}
	}
	change := NodeChange{Kind: input.Kind, Tag: input.Tag, Value: input.Value, Secret: input.Secret, ClientSecret: input.ClientSecret}
	if input.Kind == "decryption" && input.Secret != "" && input.Secret != "none" && input.ClientSecret == "" {
		return nil, errors.New("client_secret is required with custom decryption")
	}
	if !apply {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		if _, err = applyManagedNodeChange(root, change); err != nil {
			return nil, err
		}
		return agentPreview("nodes.update", map[string]string{"tag": input.Tag, "kind": input.Kind}), nil
	}
	return agentMessage(ChangeNodeConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, change))
}

func agentSetLimit(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentLimitInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	if !managedUserName.MatchString(input.User) {
		return nil, errors.New("invalid user identifier")
	}
	switch operation {
	case "limits.quota":
		if input.QuotaBytes == nil || *input.QuotaBytes < 0 || input.ExpiryUnix != nil {
			return nil, errors.New("quota_bytes must be a nonnegative integer")
		}
	case "limits.expiry":
		if input.ExpiryUnix == nil || *input.ExpiryUnix < 0 || input.QuotaBytes != nil {
			return nil, errors.New("expiry_unix must be a nonnegative integer")
		}
		if *input.ExpiryUnix > 0 && *input.ExpiryUnix <= time.Now().Unix() {
			return nil, errors.New("expiry must be in the future")
		}
	case "limits.clear":
		if input.QuotaBytes != nil || input.ExpiryUnix != nil {
			return nil, errors.New("limits.clear accepts only user")
		}
	}
	if !apply {
		return agentPreview(operation, input), nil
	}
	err := updateUserLimits(cfg.XrayConfigPath, func(users map[string]userLimit) error {
		names, err := allDirectoryNames(cfg.XrayConfigPath)
		if err != nil {
			return err
		}
		suspended, _, err := readSuspendedStore(cfg.XrayConfigPath)
		if err != nil {
			return err
		}
		found := false
		for _, name := range names {
			if name == input.User {
				found = true
				break
			}
		}
		if _, ok := suspended.Users[input.User]; ok {
			found = true
		}
		if !found {
			return errors.New("user not found")
		}
		limit := users[input.User]
		switch operation {
		case "limits.quota":
			limit.QuotaBytes = *input.QuotaBytes
			limit.Notified80 = false
			limit.NotifiedQuota = false
		case "limits.expiry":
			limit.ExpiryUnix = *input.ExpiryUnix
			limit.NotifiedExpiry = false
			limit.NotifiedExpired = false
		case "limits.clear":
			delete(users, input.User)
			return nil
		}
		if limit.empty() {
			delete(users, input.User)
		} else {
			users[input.User] = limit
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"applied": true, "user": input.User}, nil
}

func agentUpdateComponent(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if operation == "updates.check" {
		result := make(map[string]any)
		for _, component := range []string{"script", "bot", "core"} {
			version, err := remoteComponentVersion(ctx, component)
			if err != nil {
				result[component] = map[string]string{"error": err.Error()}
			} else {
				result[component] = map[string]string{"installed": currentComponentVersion(component, cfg.XrayBinPath), "available": version}
			}
		}
		return result, nil
	}
	var input agentUpdateInput
	if operation == "updates.core" {
		if err := decodeAgentInput(raw, &input); err != nil {
			return nil, err
		}
		if !safeTag.MatchString(input.Version) {
			return nil, errors.New("invalid Xray version tag")
		}
	}
	if !apply {
		return agentPreview(operation, "update or roll back a management component"), nil
	}
	switch operation {
	case "updates.script":
		commit, err := remoteComponentVersion(ctx, "script")
		if err != nil {
			return nil, err
		}
		return agentMessage(checkedScriptUpdate(ctx, commit))
	case "updates.bot":
		if err := requireInstalledAgent(); err != nil {
			return nil, err
		}
		return agentMessage(checkedBotUpdate(ctx))
	case "updates.core":
		var release struct {
			Tag        string `json:"tag_name"`
			Prerelease bool   `json:"prerelease"`
			Draft      bool   `json:"draft"`
		}
		if err := apiJSON(ctx, "https://api.github.com/repos/"+coreRepo+"/releases/tags/"+input.Version, &release); err != nil {
			return nil, err
		}
		if release.Tag != input.Version || release.Prerelease || release.Draft {
			return nil, errors.New("only published stable Xray releases are supported")
		}
		return agentMessage(checkedCoreUpdate(ctx, cfg.XrayBinPath, cfg.XrayConfigPath, cfg.StatsAPIServer, input.Version))
	case "updates.core-rollback":
		return agentMessage(checkedCoreRollback(cfg.XrayBinPath, cfg.XrayConfigPath, cfg.StatsAPIServer))
	}
	return nil, fmt.Errorf("unknown update operation %s", strings.TrimPrefix(operation, "updates."))
}
