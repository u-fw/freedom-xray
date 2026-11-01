package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

var errNativeInboundChanged = errors.New("inbound or its list changed; refresh and review it")

type nativeInboundRecord struct {
	Index       int    `json:"index"`
	ETag        string `json:"etag"`
	ListETag    string `json:"list_etag"`
	Tag         string `json:"tag,omitempty"`
	Protocol    string `json:"protocol"`
	Listen      string `json:"listen,omitempty"`
	Port        string `json:"port,omitempty"`
	ClientCount *int   `json:"client_count,omitempty"`
	Protected   bool   `json:"protected"`
	Managed     bool   `json:"managed_vless"`
}

// Values can contain credentials. Never include them in previews or logs.
type nativeInboundChange struct {
	Kind       string
	Index      int
	ETag       string
	ListETag   string
	Tag        string
	ImpactETag string
	Field      string
	Value      string
	Inbound    map[string]any
}

func nativeInboundValues(root map[string]any) ([]any, error) {
	inbounds, ok := root["inbounds"].([]any)
	if !ok {
		return nil, errors.New("inbounds must be an array")
	}
	return inbounds, nil
}

func nativeInboundListETag(root map[string]any) (string, error) {
	values, err := nativeInboundValues(root)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12]), nil
}

func safeInboundField(value string) string {
	if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n\t") || strings.Contains(value, "://") || strings.Contains(value, "?") {
		return "<hidden>"
	}
	return value
}

func nativeInboundProtectsAPI(root map[string]any, tag string) bool {
	if tag == "api-internal" {
		return true
	}
	if tag == "" {
		return false
	}
	routing, _ := getMap(root["routing"])
	rules, _ := routing["rules"].([]any)
	for _, raw := range rules {
		rule, _ := getMap(raw)
		if rule["outboundTag"] != networkAPITag(root) {
			continue
		}
		for _, inbound := range stringList(rule["inboundTag"]) {
			if inbound == tag {
				return true
			}
		}
	}
	return false
}

func nativeInboundRecords(root map[string]any) ([]nativeInboundRecord, error) {
	values, err := nativeInboundValues(root)
	if err != nil {
		return nil, err
	}
	listETag, err := nativeInboundListETag(root)
	if err != nil {
		return nil, err
	}
	result := make([]nativeInboundRecord, 0, len(values))
	for index, value := range values {
		item, ok := getMap(value)
		if !ok {
			return nil, errors.New("invalid inbound object")
		}
		etag, err := routingRuleETag(item)
		if err != nil {
			return nil, err
		}
		tag, _ := item["tag"].(string)
		protocol, _ := item["protocol"].(string)
		listen, _ := item["listen"].(string)
		record := nativeInboundRecord{Index: index + 1, ETag: etag, ListETag: listETag, Tag: safeInboundField(tag), Protocol: safeInboundField(protocol), Listen: safeInboundField(listen), Protected: nativeInboundProtectsAPI(root, tag), Managed: strings.EqualFold(protocol, "vless")}
		switch port := item["port"].(type) {
		case string:
			record.Port = safeInboundField(port)
		case json.Number:
			record.Port = port.String()
		case float64:
			record.Port = strconv.FormatFloat(port, 'f', -1, 64)
		case int:
			record.Port = strconv.Itoa(port)
		}
		if settings, ok := getMap(item["settings"]); ok {
			if clients, ok := settings["clients"].([]any); ok {
				count := len(clients)
				record.ClientCount = &count
			}
		}
		result = append(result, record)
	}
	return result, nil
}

func lookupNativeInbound(root map[string]any, index int, etag string) (map[string]any, error) {
	values, err := nativeInboundValues(root)
	if err != nil {
		return nil, err
	}
	if index < 1 || index > len(values) || len(etag) != 24 {
		return nil, errNativeInboundChanged
	}
	item, ok := getMap(values[index-1])
	if !ok {
		return nil, errNativeInboundChanged
	}
	actual, err := routingRuleETag(item)
	if err != nil {
		return nil, err
	}
	if actual != etag {
		return nil, errNativeInboundChanged
	}
	return item, nil
}

func validateNativeInbound(root map[string]any, item map[string]any, replacing int) error {
	protocol, ok := item["protocol"].(string)
	if !ok || protocol == "" || strings.TrimSpace(protocol) != protocol || len(protocol) > 64 || strings.ContainsAny(protocol, "\x00\r\n\t /") {
		return errors.New("inbound protocol is required")
	}
	if strings.EqualFold(protocol, "vless") {
		return errors.New("VLESS inbounds use the managed node workflow")
	}
	tag, ok := item["tag"].(string)
	if item["tag"] != nil && !ok {
		return errors.New("inbound tag must be text")
	}
	if len(tag) > 256 || strings.TrimSpace(tag) != tag || strings.ContainsAny(tag, "\x00\r\n\t") {
		return errors.New("invalid inbound tag")
	}
	if tag == "api-internal" {
		return errors.New("the internal API inbound is protected")
	}
	values, err := nativeInboundValues(root)
	if err != nil {
		return err
	}
	for index, value := range values {
		existing, ok := getMap(value)
		if !ok {
			return errors.New("invalid inbound object")
		}
		if index != replacing && tag != "" && existing["tag"] == tag {
			return errors.New("inbound tag already exists")
		}
	}
	return nil // The installed Xray core validates native protocol and listener details.
}

func validateNativeInboundAPI(root map[string]any, server string) error {
	api, ok := getMap(root["api"])
	if !ok || (api["listen"] == nil && len(stringList(api["services"])) == 0) {
		return nil
	}
	if server == "" {
		server = "127.0.0.1:10085"
	}
	if _, err := validateAPIAccess(root, server); err != nil {
		return fmt.Errorf("unsafe API configuration: %w", err)
	}
	return nil
}

func applyNativeInboundChange(root map[string]any, change nativeInboundChange, server string, configPaths ...string) (string, error) {
	configPath := ""
	if len(configPaths) > 0 {
		configPath = configPaths[0]
	}
	values, err := nativeInboundValues(root)
	if err != nil {
		return "", err
	}
	if change.Kind == "add" || change.Kind == "delete" {
		actual, err := nativeInboundListETag(root)
		if err != nil {
			return "", err
		}
		if change.ListETag == "" || actual != change.ListETag {
			return "", errNativeInboundChanged
		}
	}
	if change.Kind == "add" {
		if change.Inbound == nil {
			return "", errors.New("inbound object is required")
		}
		if err := validateNativeInbound(root, change.Inbound, -1); err != nil {
			return "", err
		}
		if err := validateNativeInboundIdentities(root, change.Inbound, configPath); err != nil {
			return "", err
		}
		root["inbounds"] = append(values, change.Inbound)
		if err := validateNativeInboundAPI(root, server); err != nil {
			return "", err
		}
		return "Added native inbound", nil
	}
	item, err := lookupNativeInbound(root, change.Index, change.ETag)
	if err != nil {
		return "", err
	}
	tag, _ := item["tag"].(string)
	protocol, _ := item["protocol"].(string)
	if nativeInboundProtectsAPI(root, tag) || strings.EqualFold(protocol, "vless") {
		return "", errors.New("this inbound is protected by the managed node workflow")
	}
	switch change.Kind {
	case "replace":
		if change.Inbound == nil {
			return "", errors.New("replacement inbound is required")
		}
		replacementTag, tagOK := change.Inbound["tag"].(string)
		if change.Inbound["tag"] != nil && !tagOK {
			return "", errors.New("inbound tag must be text")
		}
		replacementProtocol, protocolOK := change.Inbound["protocol"].(string)
		if !protocolOK || replacementTag != tag || replacementProtocol != protocol {
			return "", errors.New("replacement must keep its existing tag and protocol")
		}
		if err := validateNativeInbound(root, change.Inbound, change.Index-1); err != nil {
			return "", err
		}
		if err := validateNativeInboundIdentities(root, change.Inbound, configPath); err != nil {
			return "", err
		}
		values[change.Index-1] = change.Inbound
	case "port":
		if strings.EqualFold(protocol, "tun") {
			return "", errors.New("a TUN inbound does not use a listener port")
		}
		listen, _ := item["listen"].(string)
		if strings.HasPrefix(listen, "/") || strings.HasPrefix(listen, "@") {
			return "", errors.New("a Unix socket inbound does not use a TCP port")
		}
		port, err := validateListenPort(change.Value)
		if err != nil {
			return "", err
		}
		if inboundPortValue(item["port"]) == port {
			return "", errors.New("listener port is unchanged")
		}
		item["port"] = json.Number(strconv.Itoa(port))
	case "listen":
		if strings.EqualFold(protocol, "tun") {
			return "", errors.New("a TUN inbound does not use a listen address")
		}
		listen := change.Value
		if listen != "" && net.ParseIP(listen) == nil {
			return "", errors.New("listen address must be an IP or empty for all interfaces")
		}
		previous, _ := item["listen"].(string)
		if previous == listen {
			return "", errors.New("listen address is unchanged")
		}
		if listen == "" {
			delete(item, "listen")
		} else {
			item["listen"] = listen
		}
	case "delete":
		if tag != "" {
			return "", errors.New("tagged deletion requires a reviewed impact reference")
		}
		root["inbounds"] = append(values[:change.Index-1:change.Index-1], values[change.Index:]...)
		return "Deleted untagged inbound", nil
	default:
		return "", errors.New("unknown inbound operation")
	}
	if err := validateNativeInboundAPI(root, server); err != nil {
		return "", err
	}
	return "Updated native inbound", nil
}

func changeNativeInboundConfig(cfg *BotConfig, change nativeInboundChange) (string, error) {
	if change.Kind == "delete" && change.Tag != "" {
		if change.ImpactETag == "" {
			return "", errors.New("deletion impact reference is required")
		}
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return "", err
		}
		item, err := lookupNativeInbound(root, change.Index, change.ETag)
		if err != nil {
			return "", err
		}
		tag, _ := item["tag"].(string)
		protocol, _ := item["protocol"].(string)
		listETag, err := nativeInboundListETag(root)
		if err != nil || listETag != change.ListETag || tag != change.Tag {
			return "", errNativeInboundChanged
		}
		if nativeInboundProtectsAPI(root, tag) || strings.EqualFold(protocol, "vless") {
			return "", errors.New("this inbound is protected by the managed node workflow")
		}
		if err := validateNativeInboundAPI(root, cfg.StatsAPIServer); err != nil {
			return "", err
		}
		message, err := deleteNodeWithExpectedImpact(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, change.Tag, change.ImpactETag)
		return message, sanitizeNetworkChangeError(err)
	}
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) {
		return applyNativeInboundChange(root, change, cfg.StatsAPIServer, cfg.XrayConfigPath)
	})
	return message, sanitizeNetworkChangeError(err)
}

type agentNativeInboundInput struct {
	Index      int             `json:"index,omitempty"`
	ETag       string          `json:"etag,omitempty"`
	ListETag   string          `json:"list_etag,omitempty"`
	ImpactETag string          `json:"impact_etag,omitempty"`
	Field      string          `json:"field,omitempty"`
	Value      *string         `json:"value,omitempty"`
	Inbound    json.RawMessage `json:"inbound,omitempty"`
}

func agentNativeInboundChange(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentNativeInboundInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	change := nativeInboundChange{Index: input.Index, ETag: input.ETag, ListETag: input.ListETag, ImpactETag: input.ImpactETag, Field: input.Field}
	switch operation {
	case "inbounds.add", "inbounds.replace":
		if len(input.Inbound) == 0 || input.Field != "" || input.Value != nil || input.ImpactETag != "" {
			return nil, errors.New("native inbound object and edit reference are required")
		}
		value, err := decodeNetworkObject(input.Inbound)
		if err != nil {
			return nil, err
		}
		change.Inbound, change.Kind = value, "add"
		if operation == "inbounds.replace" {
			change.Kind = "replace"
			if input.Index < 1 || input.ETag == "" || input.ListETag != "" {
				return nil, errors.New("replacement requires index and etag")
			}
		} else if input.Index != 0 || input.ETag != "" || input.ListETag == "" {
			return nil, errors.New("creation requires list_etag")
		}
	case "inbounds.patch":
		if input.Index < 1 || input.ETag == "" || input.Value == nil || len(input.Inbound) != 0 || input.ListETag != "" || input.ImpactETag != "" || (input.Field != "port" && input.Field != "listen") {
			return nil, errors.New("patch requires index, etag, field and value")
		}
		change.Kind, change.Value = input.Field, *input.Value
	case "inbounds.remove":
		if input.Index < 1 || input.ETag == "" || input.ListETag == "" || input.Field != "" || input.Value != nil || len(input.Inbound) != 0 {
			return nil, errors.New("removal requires index, etag and list_etag")
		}
		change.Kind = "delete"
	default:
		return nil, errors.New("unknown inbound operation")
	}
	if change.Kind == "delete" {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		item, err := lookupNativeInbound(root, change.Index, change.ETag)
		if err != nil {
			return nil, err
		}
		listETag, err := nativeInboundListETag(root)
		if err != nil || listETag != change.ListETag {
			return nil, errNativeInboundChanged
		}
		tag, _ := item["tag"].(string)
		protocol, _ := item["protocol"].(string)
		if nativeInboundProtectsAPI(root, tag) || strings.EqualFold(protocol, "vless") {
			return nil, errors.New("this inbound is protected by the managed node workflow")
		}
		change.Tag = tag
		if tag != "" {
			report, impactETag, err := inspectNodeDeletionForConfirmation(cfg.XrayConfigPath, tag)
			if err != nil {
				return nil, err
			}
			fingerprint, err := nodeDeletionFingerprint(root, report)
			if err != nil || fingerprint != impactETag {
				return nil, errNativeInboundChanged
			}
			if change.ImpactETag != "" && change.ImpactETag != impactETag {
				return nil, errNodeDeletionChanged
			}
			if !apply {
				report.ETag = impactETag
				return agentPreview(operation, report), nil
			}
			if change.ImpactETag == "" {
				return nil, errors.New("use the preview impact etag when applying removal")
			}
		} else if input.ImpactETag != "" {
			return nil, errors.New("untagged inbound has no impact etag")
		}
	}
	if !apply {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		message, err := applyNativeInboundChange(root, change, cfg.StatsAPIServer, cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, message), nil
	}
	return agentMessage(changeNativeInboundConfig(cfg, change))
}
