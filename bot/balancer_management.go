package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var errBalancerChanged = errors.New("balancer changed; refresh the list")

type balancerRecord struct {
	Tag            string         `json:"tag"`
	ETag           string         `json:"etag"`
	Balancer       map[string]any `json:"balancer"`
	RedactedFields []string       `json:"redacted_fields,omitempty"`
}

type balancerChange struct {
	Kind     string
	Tag      string
	ETag     string
	Field    string
	Value    any
	Balancer map[string]any
}

func routingBalancers(root map[string]any) ([]any, error) {
	routing, ok := getMap(root["routing"])
	if !ok {
		if root["routing"] == nil {
			return []any{}, nil
		}
		return nil, errors.New("routing must be an object")
	}
	values, ok := routing["balancers"].([]any)
	if !ok && routing["balancers"] != nil {
		return nil, errors.New("routing.balancers must be an array")
	}
	return values, nil
}

func balancerRecords(root map[string]any) ([]balancerRecord, error) {
	values, err := routingBalancers(root)
	if err != nil {
		return nil, err
	}
	records := make([]balancerRecord, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		item, ok := getMap(value)
		if !ok {
			return nil, errors.New("invalid balancer object")
		}
		tag, ok := item["tag"].(string)
		if !ok || tag == "" || seen[tag] {
			return nil, errors.New("balancers require unique nonempty tags")
		}
		seen[tag] = true
		etag, err := routingRuleETag(item)
		if err != nil {
			return nil, err
		}
		records = append(records, balancerRecord{Tag: tag, ETag: etag, Balancer: item})
	}
	return records, nil
}

// Only well-known display fields are exposed by default. Native strategy
// settings and extensions can contain operational secrets.
func redactBalancerRecords(records []balancerRecord) []balancerRecord {
	result := make([]balancerRecord, len(records))
	for i, record := range records {
		result[i] = record
		item := make(map[string]any, len(record.Balancer))
		for key, value := range record.Balancer {
			switch key {
			case "tag", "selector", "fallbackTag":
				item[key] = value
			case "strategy":
				strategy, ok := getMap(value)
				if !ok {
					item[key] = "<hidden>"
					result[i].RedactedFields = append(result[i].RedactedFields, key)
					continue
				}
				visible := map[string]any{}
				if kind, ok := strategy["type"].(string); ok {
					visible["type"] = kind
				}
				if len(strategy) > len(visible) {
					result[i].RedactedFields = append(result[i].RedactedFields, "strategy.settings")
				}
				item[key] = visible
			default:
				item[key] = "<hidden>"
				result[i].RedactedFields = append(result[i].RedactedFields, key)
			}
		}
		sort.Strings(result[i].RedactedFields)
		result[i].Balancer = item
	}
	return result
}

func findBalancer(root map[string]any, tag string) (int, map[string]any, error) {
	values, err := routingBalancers(root)
	if err != nil {
		return -1, nil, err
	}
	index := -1
	var found map[string]any
	for i, value := range values {
		item, ok := getMap(value)
		if !ok {
			return -1, nil, errors.New("invalid balancer object")
		}
		if item["tag"] == tag {
			if index >= 0 {
				return -1, nil, errors.New("duplicate balancer tag")
			}
			index, found = i, item
		}
	}
	return index, found, nil
}

func validateBalancer(root, item map[string]any) error {
	tag, ok := item["tag"].(string)
	if !ok || strings.TrimSpace(tag) != tag || tag == "" || len(tag) > 256 || strings.ContainsAny(tag, "\x00\r\n") {
		return errors.New("invalid balancer tag")
	}
	if err := validateOutboundSelectors(root, item["selector"]); err != nil {
		return err
	}
	fallback := ""
	if value, exists := item["fallbackTag"]; exists && value != nil {
		var ok bool
		fallback, ok = value.(string)
		if !ok {
			return errors.New("fallback must be an outbound tag")
		}
		if fallback != "" && (fallback == networkAPITag(root) || !routingTargetExists(root, "outboundTag", fallback)) {
			return errors.New("fallback outbound does not exist or is protected")
		}
	}
	strategy := "random"
	if value := item["strategy"]; value != nil {
		config, ok := getMap(value)
		if !ok {
			return errors.New("strategy must be an object")
		}
		if raw, exists := config["type"]; exists {
			kind, ok := raw.(string)
			if !ok {
				return errors.New("invalid balancing strategy")
			}
			if kind != "" {
				strategy = strings.ToLower(kind)
			}
		}
	}
	switch strategy {
	case "random", "roundrobin", "leastping", "leastload":
	default:
		return errors.New("unknown balancing strategy")
	}
	if fallback != "" || strategy == "leastping" || strategy == "leastload" {
		if _, ok := getMap(root["observatory"]); !ok {
			if _, ok = getMap(root["burstObservatory"]); !ok {
				return errors.New("this strategy or fallback requires a configured observatory")
			}
		}
	}
	return nil
}

func validateOutboundSelectors(root map[string]any, value any) error {
	selectors := stringList(value)
	if len(selectors) == 0 || len(selectors) > 256 {
		return errors.New("provide between 1 and 256 outbound selectors")
	}
	if raw, ok := value.([]any); ok && len(raw) != len(selectors) {
		return errors.New("invalid outbound selector")
	}
	for _, selector := range selectors {
		if strings.TrimSpace(selector) != selector || selector == "" || len(selector) > 256 || strings.ContainsAny(selector, "\x00\r\n") {
			return errors.New("invalid outbound selector")
		}
		if strings.HasPrefix(networkAPITag(root), selector) {
			return errors.New("selector may match the protected internal API outbound")
		}
	}
	return nil
}

func applyBalancerChange(root map[string]any, change balancerChange) (string, error) {
	if change.Tag == "" {
		return "", errors.New("balancer tag is required")
	}
	index, existing, err := findBalancer(root, change.Tag)
	if err != nil {
		return "", err
	}
	if index >= 0 {
		etag, err := routingRuleETag(existing)
		if err != nil {
			return "", err
		}
		if change.ETag == "" || etag != change.ETag {
			return "", errBalancerChanged
		}
	} else if change.ETag != "" || change.Kind != "put" {
		return "", errBalancerChanged
	}
	if change.Kind == "delete" {
		routing, _ := getMap(root["routing"])
		if script, ok := routing["script"].(string); ok && script != "" {
			return "", errors.New("balancer references in a routing script cannot be checked")
		}
		rules, err := routingRules(root)
		if err != nil {
			return "", err
		}
		for _, value := range rules {
			rule, ok := getMap(value)
			if !ok {
				return "", errors.New("invalid routing rule")
			}
			if rule["balancerTag"] == change.Tag {
				return "", errors.New("balancer is referenced by a routing rule")
			}
		}
		values, _ := routingBalancers(root)
		routing["balancers"] = append(values[:index:index], values[index+1:]...)
		return fmt.Sprintf("Deleted balancer %s", change.Tag), nil
	}
	var candidate map[string]any
	switch change.Kind {
	case "put":
		candidate, err = normalizeBalancerObject(change.Balancer)
		if err != nil {
			return "", err
		}
		if candidate["tag"] != change.Tag {
			return "", errors.New("balancer tag does not match")
		}
	case "patch":
		candidate, err = normalizeBalancerObject(existing)
		if err != nil {
			return "", err
		}
		switch change.Field {
		case "selector":
			candidate["selector"] = change.Value
		case "fallbackTag":
			if change.Value == nil || change.Value == "" {
				delete(candidate, "fallbackTag")
			} else {
				candidate["fallbackTag"] = change.Value
			}
		case "strategy":
			kind, ok := change.Value.(string)
			if !ok {
				return "", errors.New("strategy must be text")
			}
			strategy := map[string]any{}
			if previous, ok := getMap(candidate["strategy"]); ok {
				for key, value := range previous {
					strategy[key] = value
				}
			}
			strategy["type"] = kind
			candidate["strategy"] = strategy
		default:
			return "", errors.New("unsupported balancer field")
		}
	default:
		return "", errors.New("unknown balancer operation")
	}
	if err := validateBalancer(root, candidate); err != nil {
		return "", err
	}
	if index >= 0 {
		before, beforeErr := json.Marshal(existing)
		after, afterErr := json.Marshal(candidate)
		if beforeErr != nil || afterErr != nil {
			return "", errors.New("cannot encode balancer")
		}
		if bytes.Equal(before, after) {
			return "", errors.New("balancer is unchanged")
		}
	}
	routing, ok := getMap(root["routing"])
	if !ok {
		routing = map[string]any{}
		root["routing"] = routing
	}
	values, err := routingBalancers(root)
	if err != nil {
		return "", err
	}
	if index < 0 {
		routing["balancers"] = append(values, candidate)
		return fmt.Sprintf("Added balancer %s", change.Tag), nil
	}
	values[index] = candidate
	routing["balancers"] = values
	return fmt.Sprintf("Updated balancer %s", change.Tag), nil
}

func changeBalancerConfig(cfg *BotConfig, change balancerChange) (string, error) {
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) { return applyBalancerChange(root, change) })
	return message, sanitizeNetworkChangeError(err)
}
