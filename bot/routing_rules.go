package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// A rule is identified by its position and a digest of its complete contents.
// An old button or automation request cannot silently edit a different rule
// after another administrator changes the order or contents.
type routingRuleRef struct {
	Index int    `json:"index"`
	ETag  string `json:"etag"`
}

type routingRuleRecord struct {
	Index          int            `json:"index"`
	ETag           string         `json:"etag"`
	ListETag       string         `json:"list_etag"`
	Protected      bool           `json:"protected"`
	Rule           map[string]any `json:"rule"`
	RedactedFields []string       `json:"redacted_fields,omitempty"`
}

type routingRuleEdit struct {
	Ref              routingRuleRef
	Kind             string // field, target, delete, move
	Field            string
	Value            any
	Direction        int
	ExpectedListETag string
}

var errRoutingRuleChanged = errors.New("routing rule changed; refresh the list")

func routingRuleETag(rule map[string]any) (string, error) {
	data, err := json.Marshal(rule)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12]), nil
}

func routingListETag(root map[string]any) (string, error) {
	rules, err := routingRules(root)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(rules)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12]), nil
}

func routingRules(root map[string]any) ([]any, error) {
	routing, ok := getMap(root["routing"])
	if !ok {
		if root["routing"] == nil {
			return []any{}, nil
		}
		return nil, errors.New("routing must be an object")
	}
	rules, ok := routing["rules"].([]any)
	if !ok && routing["rules"] != nil {
		return nil, errors.New("routing.rules must be an array")
	}
	return rules, nil
}

func protectedRoutingRule(root, rule map[string]any) bool {
	if rule == nil {
		return true
	}
	if rule["outboundTag"] == networkAPITag(root) {
		return true
	}
	for _, tag := range stringList(rule["inboundTag"]) {
		if tag == "api-internal" {
			return true
		}
	}
	return false
}

func routingHasInternalInbound(root map[string]any) bool {
	inbounds, _ := root["inbounds"].([]any)
	for _, entry := range inbounds {
		inbound, _ := getMap(entry)
		if inbound["tag"] == "api-internal" {
			return true
		}
	}
	return false
}

func routingRuleRecords(root map[string]any) ([]routingRuleRecord, error) {
	rules, err := routingRules(root)
	if err != nil {
		return nil, err
	}
	records := make([]routingRuleRecord, 0, len(rules))
	listETag, err := routingListETag(root)
	if err != nil {
		return nil, err
	}
	for i, value := range rules {
		rule, ok := getMap(value)
		if !ok {
			return nil, fmt.Errorf("routing rule %d must be an object", i+1)
		}
		etag, err := routingRuleETag(rule)
		if err != nil {
			return nil, err
		}
		records = append(records, routingRuleRecord{Index: i + 1, ETag: etag, ListETag: listETag, Protected: protectedRoutingRule(root, rule), Rule: rule})
	}
	return records, nil
}

func loadRoutingRuleRecords(path string) ([]routingRuleRecord, error) {
	root, err := decodeBindingMigrationConfig(path)
	if err != nil {
		return nil, err
	}
	return routingRuleRecords(root)
}

// Xray routing extensions may contain webhook URLs, headers, or other secrets.
// Only display established routing fields unless local callers opt in to the
// complete native object. The reference still covers the unredacted rule.
var routingPublicFields = map[string]bool{
	"type": true, "ruleTag": true, "outboundTag": true, "balancerTag": true,
	"domain": true, "domains": true, "ip": true, "port": true, "network": true,
	"protocol": true, "source": true, "sourceIP": true, "sourcePort": true,
	"localIP": true, "localPort": true, "user": true, "inboundTag": true,
	"vlessRoute": true, "process": true, "localOS": true,
}

func redactRoutingRuleRecords(records []routingRuleRecord) []routingRuleRecord {
	result := make([]routingRuleRecord, len(records))
	for i, record := range records {
		result[i] = record
		result[i].Rule = make(map[string]any, len(record.Rule))
		for field, value := range record.Rule {
			if routingPublicFields[field] {
				result[i].Rule[field] = value
			} else {
				result[i].Rule[field] = "<hidden>"
				result[i].RedactedFields = append(result[i].RedactedFields, field)
			}
		}
		sort.Strings(result[i].RedactedFields)
	}
	return result
}

func lookupRoutingRule(root map[string]any, ref routingRuleRef) (map[string]any, error) {
	if ref.Index < 1 || len(ref.ETag) != 24 {
		return nil, errors.New("invalid routing rule reference")
	}
	if _, err := hex.DecodeString(ref.ETag); err != nil {
		return nil, errors.New("invalid routing rule reference")
	}
	rules, err := routingRules(root)
	if err != nil {
		return nil, err
	}
	if ref.Index > len(rules) {
		return nil, errRoutingRuleChanged
	}
	rule, ok := getMap(rules[ref.Index-1])
	if !ok {
		return nil, errRoutingRuleChanged
	}
	etag, err := routingRuleETag(rule)
	if err != nil {
		return nil, err
	}
	if etag != ref.ETag {
		return nil, errRoutingRuleChanged
	}
	return rule, nil
}

func routingTargetExists(root map[string]any, kind, tag string) bool {
	var entries []any
	if kind == "outboundTag" {
		entries, _ = root["outbounds"].([]any)
	} else if kind == "balancerTag" {
		routing, _ := getMap(root["routing"])
		entries, _ = routing["balancers"].([]any)
	} else {
		return false
	}
	for _, entry := range entries {
		item, _ := getMap(entry)
		if item["tag"] == tag {
			return true
		}
	}
	return false
}

func routingRuleHasMatcher(rule map[string]any) bool {
	for key, value := range rule {
		if key == "domain" && rule["domains"] != nil || key == "source" && rule["sourceIP"] != nil {
			continue // Xray gives the alias precedence when both are configured.
		}
		switch key {
		case "domain", "domains", "ip", "port", "network", "protocol", "source", "sourceIP", "sourcePort", "localIP", "localPort", "user", "inboundTag", "vlessRoute":
			// Only known matching fields count. Unknown metadata may be ignored
			// by Xray and must not make an otherwise empty rule look safe.
		default:
			continue
		}
		switch item := value.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(item) != "" {
				return true
			}
		case []any:
			for _, entry := range item {
				if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}

var routingEditableFields = map[string]bool{
	"ruleTag": true, "domain": true, "domains": true, "ip": true, "port": true, "network": true, "protocol": true,
	"source": true, "sourceIP": true, "sourcePort": true, "localIP": true, "localPort": true,
	"user": true, "inboundTag": true, "vlessRoute": true,
}

func validateRoutingField(field string, value any) error {
	if !routingEditableFields[field] {
		return errors.New("unsupported routing field")
	}
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" || len(v) > 2048 || strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("invalid routing value")
		}
		if field == "ruleTag" {
			return validateNodeTag(v)
		}
		return nil
	case []any:
		if field == "ruleTag" || field == "port" || field == "sourcePort" || field == "localPort" || field == "network" || field == "vlessRoute" {
			return errors.New("this field requires a single value")
		}
		if len(v) == 0 || len(v) > 256 {
			return errors.New("provide between 1 and 256 values")
		}
		for _, entry := range v {
			text, ok := entry.(string)
			if !ok || strings.TrimSpace(text) == "" || len(text) > 2048 || strings.ContainsAny(text, "\x00\r\n") {
				return errors.New("invalid routing list value")
			}
		}
		return nil
	case json.Number:
		if field == "port" || field == "sourcePort" || field == "localPort" || field == "vlessRoute" {
			return nil
		}
	}
	return errors.New("invalid routing value type")
}

func applyRoutingRuleEdit(root map[string]any, change routingRuleEdit) (string, error) {
	if routing, ok := getMap(root["routing"]); ok {
		if script, ok := routing["script"].(string); ok && script != "" {
			return "", errors.New("routing rules cannot be edited while a routing script is configured")
		}
	}
	rules, err := routingRules(root)
	if err != nil {
		return "", err
	}
	if routingHasInternalInbound(root) {
		if len(rules) == 0 {
			return "", errors.New("repair the internal API route first")
		}
		first, _ := getMap(rules[0])
		if !isInternalAPIRoute(first, networkAPITag(root)) {
			return "", errors.New("repair the internal API route first")
		}
	}
	rule, err := lookupRoutingRule(root, change.Ref)
	if err != nil {
		return "", err
	}
	if protectedRoutingRule(root, rule) {
		return "", errors.New("the internal API route is protected")
	}
	routing, _ := getMap(root["routing"])
	index := change.Ref.Index - 1
	switch change.Kind {
	case "delete":
		routing["rules"] = append(rules[:index:index], rules[index+1:]...)
		return fmt.Sprintf("Deleted routing rule %d", change.Ref.Index), nil
	case "move":
		if change.ExpectedListETag != "" {
			current, e := routingListETag(root)
			if e != nil {
				return "", e
			}
			if current != change.ExpectedListETag {
				return "", errRoutingRuleChanged
			}
		}
		if change.Direction != 1 && change.Direction != -1 {
			return "", errors.New("direction must be up or down")
		}
		next := index + change.Direction
		if next < 0 || next >= len(rules) {
			return "", errors.New("rule is already at the edge of the list")
		}
		other, _ := getMap(rules[next])
		if protectedRoutingRule(root, other) {
			return "", errors.New("the internal API rule must remain protected")
		}
		rules[index], rules[next] = rules[next], rules[index]
		routing["rules"] = rules
		return fmt.Sprintf("Moved routing rule %d to %d", index+1, next+1), nil
	case "target":
		tag, ok := change.Value.(string)
		if !ok || tag == "" || !routingTargetExists(root, change.Field, tag) {
			return "", errors.New("target outbound or balancer does not exist")
		}
		if change.Field == "outboundTag" && tag == networkAPITag(root) {
			return "", errors.New("the internal API outbound is protected")
		}
		if rule[change.Field] == tag {
			return "", errors.New("target is unchanged")
		}
		delete(rule, "outboundTag")
		delete(rule, "balancerTag")
		rule[change.Field] = tag
	case "field":
		if err := validateRoutingField(change.Field, change.Value); err != nil {
			return "", err
		}
		if change.Field == "inboundTag" {
			for _, tag := range stringList(change.Value) {
				if tag == "api-internal" {
					return "", errors.New("the internal API inbound is protected")
				}
			}
		}
		if change.Field == "ruleTag" && change.Value != nil {
			for i, value := range rules {
				other, _ := getMap(value)
				if i != index && other["ruleTag"] == change.Value {
					return "", errors.New("routing rule tag already exists")
				}
			}
		}
		before, _ := json.Marshal(rule[change.Field])
		after, _ := json.Marshal(change.Value)
		if bytes.Equal(before, after) {
			return "", errors.New("field is unchanged")
		}
		hadMatcher := routingRuleHasMatcher(rule)
		previous, existed := rule[change.Field]
		if change.Value == nil {
			delete(rule, change.Field)
		} else {
			rule[change.Field] = change.Value
		}
		if hadMatcher && !routingRuleHasMatcher(rule) {
			if existed {
				rule[change.Field] = previous
			} else {
				delete(rule, change.Field)
			}
			return "", errors.New("cannot remove the last matching condition")
		}
	default:
		return "", errors.New("unknown routing rule operation")
	}
	return fmt.Sprintf("Updated routing rule %d", change.Ref.Index), nil
}

func changeRoutingRule(cfg *BotConfig, change routingRuleEdit) (string, error) {
	return commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) { return applyRoutingRuleEdit(root, change) })
}

func applyGuidedRoutingAdd(root map[string]any, change routingChange) (string, error) {
	if routing, ok := getMap(root["routing"]); ok {
		if script, ok := routing["script"].(string); ok && script != "" {
			return "", errors.New("routing rules cannot be edited while a routing script is configured")
		}
	}
	if root["routing"] == nil {
		root["routing"] = map[string]any{"rules": []any{}}
	}
	routing, ok := getMap(root["routing"])
	if !ok {
		return "", errors.New("routing must be an object")
	}
	if routing["rules"] == nil {
		routing["rules"] = []any{}
	}
	return applyRoutingChange(root, change)
}

func decodeRoutingEditValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected one JSON value")
	}
	return value, nil
}
