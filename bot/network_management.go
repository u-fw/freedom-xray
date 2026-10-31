package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// networkChange contains native Xray configuration. Never include Value in
// previews, log messages or confirmations: outbounds and DNS may hold secrets.
type networkChange struct {
	Kind, Tag string
	Value     map[string]any
}

func decodeNetworkObject(raw []byte) (map[string]any, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("JSON input exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("expected a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected exactly one JSON object")
	}
	return object, nil
}

func networkAPITag(root map[string]any) string {
	api, _ := getMap(root["api"])
	tag, _ := api["tag"].(string)
	if tag == "" {
		return "api"
	}
	return tag
}

func containsNetworkReference(value any, tag string) bool {
	switch entry := value.(type) {
	case string:
		return entry == tag
	case []any:
		for _, child := range entry {
			if containsNetworkReference(child, tag) {
				return true
			}
		}
	case map[string]any:
		for _, child := range entry {
			if containsNetworkReference(child, tag) {
				return true
			}
		}
	}
	return false
}

func applyNetworkChange(root map[string]any, change networkChange) (string, error) {
	if change.Kind == "dns.put" {
		if change.Value == nil {
			return "", errors.New("DNS configuration must be an object")
		}
		root["dns"] = change.Value
		return "Updated Xray DNS configuration", nil
	}
	if change.Kind != "outbounds.put" && change.Kind != "outbounds.delete" {
		return "", errors.New("unknown network operation")
	}
	if err := validateNodeTag(change.Tag); err != nil {
		return "", fmt.Errorf("outbound tag: %w", err)
	}
	if change.Tag == networkAPITag(root) {
		return "", errors.New("the internal API outbound is protected")
	}
	outbounds, ok := root["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		return "", errors.New("at least one outbound is required")
	}
	index := -1
	for i, entry := range outbounds {
		outbound, ok := getMap(entry)
		if !ok {
			return "", errors.New("invalid existing outbound")
		}
		if outbound["tag"] == change.Tag {
			if index >= 0 {
				return "", errors.New("duplicate outbound tag")
			}
			index = i
		}
	}
	if change.Kind == "outbounds.delete" {
		if index < 0 {
			return "", errors.New("outbound not found")
		}
		if index == 0 {
			return "", errors.New("the default outbound cannot be deleted")
		}
		if routing, ok := getMap(root["routing"]); ok {
			if balancers, ok := routing["balancers"].([]any); ok && len(balancers) > 0 {
				return "", errors.New("cannot safely delete an outbound while routing balancers exist")
			}
		}
		// Conservatively detect references in every remaining config section,
		// including routing rules, proxySettings and dialerProxy.
		for key, value := range root {
			if key == "outbounds" {
				continue
			}
			if containsNetworkReference(value, change.Tag) {
				return "", errors.New("outbound is referenced by the Xray configuration")
			}
		}
		for i, value := range outbounds {
			if i != index && containsNetworkReference(value, change.Tag) {
				return "", errors.New("outbound is referenced by another outbound")
			}
		}
		root["outbounds"] = append(outbounds[:index:index], outbounds[index+1:]...)
		return fmt.Sprintf("Deleted outbound %s", change.Tag), nil
	}
	if change.Value == nil || change.Value["tag"] != change.Tag {
		return "", errors.New("outbound tag is required and must match")
	}
	protocol, ok := change.Value["protocol"].(string)
	if !ok || validateNodeTag(protocol) != nil {
		return "", errors.New("invalid outbound protocol")
	}
	if index < 0 {
		root["outbounds"] = append(outbounds, change.Value)
		return fmt.Sprintf("Added outbound %s (%s)", change.Tag, protocol), nil
	}
	outbounds[index] = change.Value
	root["outbounds"] = outbounds
	return fmt.Sprintf("Updated outbound %s (%s)", change.Tag, protocol), nil
}

func changeNetworkConfig(cfg *BotConfig, change networkChange) (string, error) {
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) { return applyNetworkChange(root, change) })
	if err == nil {
		return message, nil
	}
	// Core diagnostics can echo credential-bearing input. Preserve the outcome
	// and rollback status without forwarding those diagnostics to Telegram/JSON.
	switch {
	case strings.HasPrefix(err.Error(), "Xray configuration validation failed:"):
		return "", errors.New("Xray rejected the candidate configuration; the existing configuration was not changed")
	case strings.HasPrefix(err.Error(), "Xray restart failed ("):
		return "", errors.New("Xray restart failed and configuration restoration also failed; inspect backups and service status immediately")
	case strings.HasPrefix(err.Error(), "Original configuration restored, but Xray restart failed:"):
		return "", errors.New("The original configuration was restored, but Xray could not be restarted; inspect service status")
	case strings.HasPrefix(err.Error(), "New configuration failed startup verification"):
		return "", errors.New("The new configuration failed startup verification and was rolled back")
	default:
		return "", err
	}
}

func networkSummary(configPath string) ([]map[string]any, map[string]any, error) {
	root, err := decodeBindingMigrationConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	entries, ok := root["outbounds"].([]any)
	if !ok {
		return nil, nil, errors.New("outbounds must be an array")
	}
	result := make([]map[string]any, 0, len(entries))
	for i, value := range entries {
		outbound, ok := getMap(value)
		if !ok {
			return nil, nil, errors.New("invalid outbound")
		}
		tag, _ := outbound["tag"].(string)
		protocol, _ := outbound["protocol"].(string)
		result = append(result, map[string]any{"index": i + 1, "tag": tag, "protocol": protocol, "default": i == 0})
	}
	dns, exists := getMap(root["dns"])
	servers, _ := dns["servers"].([]any)
	return result, map[string]any{"configured": exists, "server_count": len(servers)}, nil
}
