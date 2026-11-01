package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var nativeClientEmail = regexp.MustCompile(`^[A-Za-z0-9_.@+\-]{1,128}$`)

type nativeClientRecord struct {
	Index int    `json:"index"`
	Email string `json:"email,omitempty"`
	Level string `json:"level"`
}

type nativeClientChange struct {
	Kind         string
	InboundIndex int
	InboundETag  string
	ClientIndex  int
	Value        string
	RoutingETag  string
}

type agentNativeClientInput struct {
	Index       int    `json:"index"`
	ETag        string `json:"etag"`
	ClientIndex int    `json:"client_index,omitempty"`
	Field       string `json:"field,omitempty"`
	Value       string `json:"value,omitempty"`
	RoutingETag string `json:"routing_etag,omitempty"`
}

func lookupNativeClientInbound(root map[string]any, index int, etag string) (map[string]any, string, error) {
	inbound, err := lookupNativeInbound(root, index, etag)
	if err != nil {
		return nil, "", err
	}
	tag, _ := inbound["tag"].(string)
	protocol, _ := inbound["protocol"].(string)
	if nativeInboundProtectsAPI(root, tag) {
		return nil, "", errors.New("the internal API inbound is protected")
	}
	if protocol != "vmess" && protocol != "trojan" {
		return nil, "", errors.New("guided client management supports VMess and Trojan inbounds")
	}
	return inbound, protocol, nil
}

func nativeClientValues(inbound map[string]any) ([]any, error) {
	if inbound["settings"] == nil {
		return []any{}, nil
	}
	return clientList(inbound)
}

func nativeClientRecords(inbound map[string]any) ([]nativeClientRecord, error) {
	values, err := nativeClientValues(inbound)
	if err != nil {
		return nil, err
	}
	records := make([]nativeClientRecord, 0, len(values))
	for i, value := range values {
		client, ok := getMap(value)
		if !ok {
			return nil, errors.New("client must be an object")
		}
		email, _ := client["email"].(string)
		records = append(records, nativeClientRecord{Index: i + 1, Email: safeInboundField(email), Level: safeInboundField(clientLevelString(client["level"]))})
	}
	return records, nil
}

func lookupNativeClient(inbound map[string]any, index int) (map[string]any, error) {
	values, err := nativeClientValues(inbound)
	if err != nil {
		return nil, err
	}
	if index < 1 || index > len(values) {
		return nil, errNativeInboundChanged
	}
	client, ok := getMap(values[index-1])
	if !ok {
		return nil, errors.New("client must be an object")
	}
	return client, nil
}

func nativeClientRoutingReferences(root map[string]any, email string) ([]int, error) {
	routing, _ := getMap(root["routing"])
	if script, ok := routing["script"].(string); ok && script != "" {
		return nil, errors.New("client references in a routing script cannot be checked")
	}
	rules, err := routingRules(root)
	if err != nil {
		return nil, err
	}
	var result []int
	if email == "" {
		return result, nil
	}
	for i, raw := range rules {
		rule, _ := getMap(raw)
		for _, name := range stringList(rule["user"]) {
			if name == email {
				result = append(result, i+1)
				break
			}
		}
	}
	return result, nil
}

func nativeClientCredential(protocol string) (string, string, error) {
	if protocol == "vmess" {
		id, err := newManagedUUID()
		return "id", id, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", "", err
	}
	return "password", base64.RawURLEncoding.EncodeToString(secret[:]), nil
}

func applyNativeClientChange(root map[string]any, change nativeClientChange, cfg *BotConfig) (map[string]any, error) {
	inbound, protocol, err := lookupNativeClientInbound(root, change.InboundIndex, change.InboundETag)
	if err != nil {
		return nil, err
	}
	values, err := nativeClientValues(inbound)
	if err != nil {
		return nil, err
	}
	var client map[string]any
	var oldEmail string
	if change.Kind != "add" {
		client, err = lookupNativeClient(inbound, change.ClientIndex)
		if err != nil {
			return nil, err
		}
		oldEmail, _ = client["email"].(string)
	}
	preview := map[string]any{"action": change.Kind, "inbound_index": change.InboundIndex, "client_index": change.ClientIndex}
	if change.Kind != "add" {
		preview["previous_email"] = safeInboundField(oldEmail)
	}
	if change.Kind == "email" || change.Kind == "remove" {
		routingETag, err := routingListETag(root)
		if err != nil {
			return nil, err
		}
		if change.RoutingETag != "" && change.RoutingETag != routingETag {
			return nil, errRoutingRuleChanged
		}
		references, err := nativeClientRoutingReferences(root, oldEmail)
		if err != nil {
			return nil, err
		}
		preview["routing_etag"], preview["routing_rules"] = routingETag, references
	}
	if change.Kind == "add" || change.Kind == "email" {
		if !nativeClientEmail.MatchString(change.Value) {
			return nil, errors.New("email must be 1–128 ASCII letters, digits, _, ., @, +, or -")
		}
		if change.Kind == "email" && oldEmail == change.Value {
			return nil, errors.New("email is unchanged")
		}
		for i, value := range values {
			other, ok := getMap(value)
			if !ok {
				return nil, errors.New("client must be an object")
			}
			if i+1 != change.ClientIndex && other["email"] == change.Value {
				return nil, errors.New("email already exists in this inbound")
			}
		}
		candidate := map[string]any{"protocol": protocol, "settings": map[string]any{"clients": []any{map[string]any{"email": change.Value}}}}
		if err := validateNativeInboundIdentities(root, candidate, cfg.XrayConfigPath); err != nil {
			return nil, err
		}
		preview["email"] = change.Value
	}
	switch change.Kind {
	case "add":
		field, secret, err := nativeClientCredential(protocol)
		if err != nil {
			return nil, err
		}
		if inbound["settings"] == nil {
			inbound["settings"] = map[string]any{}
		}
		if err := setClientList(inbound, append(values, map[string]any{"email": change.Value, field: secret})); err != nil {
			return nil, err
		}
	case "email":
		client["email"] = change.Value
	case "level":
		level, err := strconv.ParseUint(change.Value, 10, 32)
		if err != nil {
			return nil, errors.New("level must be an unsigned 32-bit integer")
		}
		if clientLevelString(client["level"]) == strconv.FormatUint(level, 10) {
			return nil, errors.New("level is unchanged")
		}
		client["level"] = json.Number(strconv.FormatUint(level, 10))
		preview["level"] = strconv.FormatUint(level, 10)
	case "rotate":
		field, secret, err := nativeClientCredential(protocol)
		if err != nil {
			return nil, err
		}
		client[field] = secret
	case "remove":
		values = append(values[:change.ClientIndex-1:change.ClientIndex-1], values[change.ClientIndex:]...)
		if err := setClientList(inbound, values); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unknown client operation")
	}
	if err := validateNativeInboundAPI(root, cfg.StatsAPIServer); err != nil {
		return nil, err
	}
	// Ensure newly selected levels have the same traffic policy as other users.
	enableStatsPolicy(root)
	return preview, nil
}

func changeNativeClientConfig(cfg *BotConfig, change nativeClientChange) (string, error) {
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) {
		if (change.Kind == "email" || change.Kind == "remove") && change.RoutingETag == "" {
			return "", errors.New("review routing impact before applying")
		}
		_, err := applyNativeClientChange(root, change, cfg)
		return "Updated native client", err
	})
	return message, sanitizeNetworkChangeError(err)
}

func agentNativeClients(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentNativeClientInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	if input.Index < 1 || input.ETag == "" {
		return nil, errors.New("inbound index and etag are required")
	}
	root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
	if err != nil {
		return nil, err
	}
	inbound, protocol, err := lookupNativeClientInbound(root, input.Index, input.ETag)
	if err != nil {
		return nil, err
	}
	if operation == "inbounds.clients.list" {
		if input.ClientIndex != 0 || input.Field != "" || input.Value != "" || input.RoutingETag != "" {
			return nil, errors.New("client list needs only an inbound reference")
		}
		records, err := nativeClientRecords(inbound)
		return map[string]any{"protocol": protocol, "index": input.Index, "etag": input.ETag, "clients": records}, err
	}
	if operation == "inbounds.clients.export" {
		if input.ClientIndex < 1 || input.Field != "" || input.Value != "" || input.RoutingETag != "" {
			return nil, errors.New("client export requires inbound and client references")
		}
		return lookupNativeClient(inbound, input.ClientIndex)
	}
	change := nativeClientChange{InboundIndex: input.Index, InboundETag: input.ETag, ClientIndex: input.ClientIndex, Value: input.Value, RoutingETag: input.RoutingETag}
	switch operation {
	case "inbounds.clients.add":
		if input.ClientIndex != 0 || input.Field != "" || input.Value == "" || input.RoutingETag != "" {
			return nil, errors.New("add requires an email and inbound reference")
		}
		change.Kind = "add"
	case "inbounds.clients.patch":
		if input.ClientIndex < 1 || (input.Field != "email" && input.Field != "level") || input.Value == "" || (input.Field == "level" && input.RoutingETag != "") {
			return nil, errors.New("patch requires a client, field and value")
		}
		change.Kind = input.Field
	case "inbounds.clients.rotate", "inbounds.clients.remove":
		if input.ClientIndex < 1 || input.Field != "" || input.Value != "" || (strings.HasSuffix(operation, "rotate") && input.RoutingETag != "") {
			return nil, errors.New("operation requires inbound and client references")
		}
		change.Kind = strings.TrimPrefix(operation, "inbounds.clients.")
	default:
		return nil, errors.New("unknown native client operation")
	}
	if !apply {
		preview, err := applyNativeClientChange(root, change, cfg)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, preview), nil
	}
	return agentMessage(changeNativeClientConfig(cfg, change))
}

func nativeClientDescription(record nativeClientRecord) string {
	if record.Email == "" {
		return fmt.Sprintf("#%d", record.Index)
	}
	return fmt.Sprintf("#%d %s", record.Index, record.Email)
}
