package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type routingChange struct {
	Kind     string
	RuleTag  string
	Rule     map[string]any
	Position int // 1-based insertion position; zero appends.
}

func isInternalAPIRoute(rule map[string]any, apiTag string) bool {
	if rule == nil || rule["outboundTag"] != apiTag {
		return false
	}
	for key := range rule {
		if key != "type" && key != "ruleTag" && key != "inboundTag" && key != "outboundTag" {
			return false
		}
	}
	if kind, exists := rule["type"]; exists && kind != "field" {
		return false
	}
	tags := stringList(rule["inboundTag"])
	return len(tags) == 1 && tags[0] == "api-internal"
}

// decodeRoutingRule accepts native Xray field rules without exposing the
// complete root configuration or its credential-bearing outbound settings.
func decodeRoutingRule(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var rule map[string]any
	if err := decoder.Decode(&rule); err != nil {
		return nil, fmt.Errorf("invalid routing rule JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("routing input must contain one JSON object")
	}
	if rule == nil {
		return nil, errors.New("routing rule must be an object")
	}
	allowed := map[string]bool{"type": true, "ruleTag": true, "outboundTag": true, "balancerTag": true, "domain": true, "ip": true, "port": true, "network": true, "protocol": true, "source": true, "sourceIP": true, "sourcePort": true, "localIP": true, "localPort": true, "user": true, "inboundTag": true, "vlessRoute": true}
	for key, value := range rule {
		if !allowed[key] {
			return nil, fmt.Errorf("unsupported routing field %q", key)
		}
		switch item := value.(type) {
		case string:
			if len(item) > 2048 || strings.ContainsAny(item, "\x00\r\n") {
				return nil, fmt.Errorf("invalid value for %s", key)
			}
		case json.Number:
			if key != "port" && key != "sourcePort" && key != "localPort" && key != "vlessRoute" {
				return nil, fmt.Errorf("%s must be text or a list", key)
			}
		case []any:
			if len(item) > 256 {
				return nil, fmt.Errorf("too many values for %s", key)
			}
			for _, entry := range item {
				text, ok := entry.(string)
				if !ok || len(text) > 2048 || strings.ContainsAny(text, "\x00\r\n") {
					return nil, fmt.Errorf("invalid list value for %s", key)
				}
			}
		default:
			return nil, fmt.Errorf("invalid value type for %s", key)
		}
	}
	tag, _ := rule["ruleTag"].(string)
	if err := validateNodeTag(tag); err != nil {
		return nil, fmt.Errorf("ruleTag: %w", err)
	}
	if value, exists := rule["type"]; exists {
		kind, ok := value.(string)
		if !ok || (kind != "" && kind != "field") {
			return nil, errors.New("only field routing rules are supported")
		}
	}
	rule["type"] = "field"
	outbound, _ := rule["outboundTag"].(string)
	balancer, _ := rule["balancerTag"].(string)
	if (outbound == "") == (balancer == "") {
		return nil, errors.New("set exactly one of outboundTag or balancerTag")
	}
	matched := false
	for _, key := range []string{"domain", "ip", "port", "network", "protocol", "source", "sourceIP", "sourcePort", "localIP", "localPort", "user", "inboundTag", "vlessRoute"} {
		if value, exists := rule[key]; exists && len(stringList(value)) > 0 {
			matched = true
		}
		if _, ok := rule[key].(json.Number); ok {
			matched = true
		}
	}
	if !matched {
		return nil, errors.New("rule must specify at least one matching condition")
	}
	for _, tag := range stringList(rule["inboundTag"]) {
		if tag == "api-internal" {
			return nil, errors.New("the internal API inbound is protected")
		}
	}
	return rule, nil
}

func applyRoutingChange(root map[string]any, change routingChange) (string, error) {
	if change.Position < 0 {
		return "", errors.New("routing position must not be negative")
	}
	if err := validateNodeTag(change.RuleTag); err != nil {
		return "", fmt.Errorf("rule tag: %w", err)
	}
	routing, ok := getMap(root["routing"])
	if !ok {
		return "", errors.New("routing must be configured first")
	}
	rules, ok := routing["rules"].([]any)
	if !ok && routing["rules"] != nil {
		return "", errors.New("routing.rules must be an array")
	}
	apiTag := "api"
	if api, ok := getMap(root["api"]); ok {
		if value, ok := api["tag"].(string); ok && value != "" {
			apiTag = value
		}
	}
	hasAPIInbound := false
	if inbounds, ok := root["inbounds"].([]any); ok {
		for _, value := range inbounds {
			in, _ := getMap(value)
			if in["tag"] == "api-internal" {
				hasAPIInbound = true
			}
		}
	}
	if hasAPIInbound {
		if len(rules) == 0 {
			return "", errors.New("repair the internal API route before editing routing rules")
		}
		first, _ := getMap(rules[0])
		if !isInternalAPIRoute(first, apiTag) {
			return "", errors.New("repair the internal API route before editing routing rules")
		}
	}
	index := -1
	for i, value := range rules {
		rule, ok := getMap(value)
		if !ok {
			return "", errors.New("invalid existing routing rule")
		}
		if rule["ruleTag"] != change.RuleTag {
			continue
		}
		if index >= 0 {
			return "", errors.New("duplicate routing rule tag")
		}
		index = i
	}
	if change.Kind == "add" && index >= 0 {
		return "", errors.New("routing rule tag already exists")
	}
	if (change.Kind == "update" || change.Kind == "delete") && index < 0 {
		return "", errors.New("routing rule not found")
	}
	if index >= 0 {
		existing, _ := getMap(rules[index])
		for _, tag := range stringList(existing["inboundTag"]) {
			if tag == "api-internal" {
				return "", errors.New("the internal API route is protected")
			}
		}
		if existing["outboundTag"] == apiTag {
			return "", errors.New("the internal API route is protected")
		}
	}
	if change.Kind == "delete" {
		routing["rules"] = append(rules[:index:index], rules[index+1:]...)
		return fmt.Sprintf("Deleted routing rule %s", change.RuleTag), nil
	}
	if change.Kind != "add" && change.Kind != "update" {
		return "", errors.New("unknown routing action")
	}
	if change.Rule == nil || change.Rule["ruleTag"] != change.RuleTag {
		return "", errors.New("ruleTag does not match the requested rule")
	}
	encoded, err := json.Marshal(change.Rule)
	if err != nil {
		return "", err
	}
	change.Rule, err = decodeRoutingRule(encoded)
	if err != nil {
		return "", err
	}
	outbound, _ := change.Rule["outboundTag"].(string)
	balancer, _ := change.Rule["balancerTag"].(string)
	if outbound == apiTag {
		return "", errors.New("the internal API outbound is protected")
	}
	targetFound := false
	if outbound != "" {
		if values, ok := root["outbounds"].([]any); ok {
			for _, value := range values {
				item, _ := getMap(value)
				if item["tag"] == outbound {
					targetFound = true
				}
			}
		}
	}
	if balancer != "" {
		if values, ok := routing["balancers"].([]any); ok {
			for _, value := range values {
				item, _ := getMap(value)
				if item["tag"] == balancer {
					targetFound = true
				}
			}
		}
	}
	if !targetFound {
		return "", errors.New("target outbound or balancer does not exist")
	}
	if change.Kind == "update" {
		rules[index] = change.Rule
		routing["rules"] = rules
		return fmt.Sprintf("Updated routing rule %s", change.RuleTag), nil
	}
	position := len(rules)
	if change.Position > 0 {
		position = change.Position - 1
	}
	if position < 0 || position > len(rules) {
		return "", errors.New("routing position is outside the rule list")
	}
	if hasAPIInbound && position == 0 {
		return "", errors.New("the internal API rule must stay first")
	}
	next := make([]any, 0, len(rules)+1)
	next = append(next, rules[:position]...)
	next = append(next, change.Rule)
	next = append(next, rules[position:]...)
	routing["rules"] = next
	return fmt.Sprintf("Added routing rule %s at position %d", change.RuleTag, position+1), nil
}

func changeRoutingConfig(configPath, xrayBinary, statsServer string, change routingChange) (string, error) {
	return commitManagedConfig(configPath, xrayBinary, statsServer, func(root map[string]any) (string, error) { return applyRoutingChange(root, change) })
}
