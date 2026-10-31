package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// applyStatsInfrastructure creates only the loopback StatsService scaffold.
// Existing ambiguous or public API listeners are rejected rather than changed.
func applyStatsInfrastructure(root map[string]any, server string) (bool, error) {
	host, portText, err := net.SplitHostPort(server)
	if err != nil {
		return false, fmt.Errorf("invalid Stats API address: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(portText)
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return false, errors.New("Stats API address must be a loopback IP and valid port")
	}
	changed := false
	object := func(parent map[string]any, key string) (map[string]any, error) {
		value, exists := parent[key]
		if !exists || value == nil {
			created := map[string]any{}
			parent[key] = created
			changed = true
			return created, nil
		}
		result, ok := getMap(value)
		if !ok {
			return nil, fmt.Errorf("%s must be an object", key)
		}
		return result, nil
	}
	if _, err = object(root, "stats"); err != nil {
		return false, err
	}
	api, err := object(root, "api")
	if err != nil {
		return false, err
	}
	apiTag, tagOK := api["tag"].(string)
	if api["tag"] != nil && !tagOK {
		return false, errors.New("api.tag must be a string")
	}
	if apiTag == "" {
		apiTag = "api"
		api["tag"] = apiTag
		changed = true
	}
	services, ok := api["services"].([]any)
	if !ok && api["services"] != nil {
		return false, errors.New("api.services must be an array")
	}
	containsService := false
	for _, entry := range services {
		if _, ok := entry.(string); !ok {
			return false, errors.New("api.services entries must be strings")
		}
		if entry == "StatsService" {
			containsService = true
		}
	}
	if !containsService {
		api["services"] = append(services, "StatsService")
		changed = true
	}
	policy, err := object(root, "policy")
	if err != nil {
		return false, err
	}
	levels, err := object(policy, "levels")
	if err != nil {
		return false, err
	}
	if _, err = object(policy, "system"); err != nil {
		return false, err
	}
	for name, value := range levels {
		if value != nil {
			if _, ok := getMap(value); !ok {
				return false, fmt.Errorf("policy level %s must be an object", name)
			}
		}
	}
	if enableStatsPolicy(root) {
		changed = true
	}
	inbounds, ok := root["inbounds"].([]any)
	if !ok {
		return false, errors.New("inbounds must be an array")
	}
	found := false
	for _, entry := range inbounds {
		inbound, ok := getMap(entry)
		if !ok {
			return false, errors.New("invalid inbound")
		}
		if inbound["tag"] == "api-internal" {
			if found {
				return false, errors.New("duplicate api-internal inbounds")
			}
			found = true
			listen, _ := inbound["listen"].(string)
			listenIP := net.ParseIP(listen)
			if listenIP == nil || !listenIP.IsLoopback() || !listenIP.Equal(ip) || inboundPortValue(inbound["port"]) != port || inbound["protocol"] != "dokodemo-door" {
				return false, errors.New("existing api-internal is not the expected loopback listener; inspect it locally")
			}
			settings, ok := getMap(inbound["settings"])
			address, _ := settings["address"].(string)
			addressIP := net.ParseIP(address)
			if !ok || addressIP == nil || !addressIP.IsLoopback() {
				return false, errors.New("existing api-internal destination must be a loopback IP")
			}
		} else if inboundPortValue(inbound["port"]) == port {
			return false, fmt.Errorf("Stats API port %d is already used by another inbound", port)
		}
	}
	if !found {
		inbound := map[string]any{"tag": "api-internal", "listen": host, "port": port, "protocol": "dokodemo-door", "settings": map[string]any{"address": host}}
		root["inbounds"] = append([]any{inbound}, inbounds...)
		changed = true
	}
	outbounds, ok := root["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		return false, errors.New("at least one existing outbound is required")
	}
	hasAPI := false
	for _, entry := range outbounds {
		outbound, _ := getMap(entry)
		if outbound["tag"] == apiTag {
			hasAPI = true
		}
	}
	if !hasAPI {
		root["outbounds"] = append(outbounds, map[string]any{"tag": apiTag, "protocol": "freedom"})
		changed = true
	}
	routing, err := object(root, "routing")
	if err != nil {
		return false, err
	}
	rules, ok := routing["rules"].([]any)
	if !ok && routing["rules"] != nil {
		return false, errors.New("routing.rules must be an array")
	}
	firstIsAPI := false
	if len(rules) > 0 {
		rule, _ := getMap(rules[0])
		firstIsAPI = isInternalAPIRoute(rule, apiTag)
	}
	if !firstIsAPI {
		routing["rules"] = append([]any{map[string]any{"type": "field", "inboundTag": []any{"api-internal"}, "outboundTag": apiTag}}, rules...)
		changed = true
	}
	return changed, nil
}

func ensureStatsInfrastructure(cfg *BotConfig) error {
	_, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) {
		changed, err := applyStatsInfrastructure(root, cfg.StatsAPIServer)
		if err != nil {
			return "", err
		}
		if !changed {
			return "", errNoConfigChanges
		}
		return "Statistics API and policies enabled", nil
	})
	if errors.Is(err, errNoConfigChanges) {
		return nil
	}
	return err
}
