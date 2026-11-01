package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var errDNSChanged = errors.New("DNS configuration changed; refresh the list")

type dnsServerSummary struct {
	Index   int    `json:"index"`
	Address string `json:"address"`
	Object  bool   `json:"object"`
}

type dnsSummary struct {
	Configured       bool               `json:"configured"`
	ETag             string             `json:"etag"`
	Servers          []dnsServerSummary `json:"servers"`
	QueryStrategy    string             `json:"query_strategy,omitempty"`
	ScriptConfigured bool               `json:"script_configured"`
	Flags            map[string]bool    `json:"flags"`
}

type dnsChange struct {
	Kind   string
	ETag   string
	Index  int
	Value  string
	Field  string
	Switch bool
}

var editableDNSFlags = map[string]bool{
	"disableCache": true, "disableFallback": true, "disableFallbackIfMatch": true,
	"enableParallelQuery": true, "useSystemHosts": true,
}

func readDNS(root map[string]any) (map[string]any, error) {
	if root["dns"] == nil {
		return nil, nil
	}
	config, ok := getMap(root["dns"])
	if !ok {
		return nil, errors.New("dns must be an object")
	}
	return config, nil
}

func dnsETag(root map[string]any) (string, error) {
	config, err := readDNS(root)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:12]), nil
}

func dnsServers(config map[string]any) ([]any, error) {
	if config == nil || config["servers"] == nil {
		return []any{}, nil
	}
	servers, ok := config["servers"].([]any)
	if !ok {
		return nil, errors.New("dns.servers must be an array")
	}
	return servers, nil
}

func safeDNSAddress(address string) string {
	if address == "" || len(address) > 2048 || strings.ContainsAny(address, "\x00\r\n\t ") {
		return "<hidden>"
	}
	if strings.Contains(address, "://") {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Hostname() == "" {
			return "<hidden>"
		}
		host := parsed.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if port := parsed.Port(); port != "" {
			host += ":" + port
		}
		return parsed.Scheme + "://" + host + "/…"
	}
	if strings.ContainsAny(address, "@/?#") {
		return "<hidden>"
	}
	return address
}

func dnsView(root map[string]any) (dnsSummary, error) {
	config, err := readDNS(root)
	if err != nil {
		return dnsSummary{}, err
	}
	etag, err := dnsETag(root)
	if err != nil {
		return dnsSummary{}, err
	}
	servers, err := dnsServers(config)
	if err != nil {
		return dnsSummary{}, err
	}
	result := dnsSummary{Configured: config != nil, ETag: etag, Servers: make([]dnsServerSummary, 0, len(servers)), Flags: map[string]bool{}}
	result.QueryStrategy, _ = config["queryStrategy"].(string)
	script, _ := config["script"].(string)
	result.ScriptConfigured = script != ""
	for name := range editableDNSFlags {
		result.Flags[name], _ = config[name].(bool)
	}
	for index, value := range servers {
		item := dnsServerSummary{Index: index + 1}
		if address, ok := value.(string); ok {
			item.Address = safeDNSAddress(address)
		} else if object, ok := getMap(value); ok {
			item.Object = true
			address, _ := object["address"].(string)
			item.Address = safeDNSAddress(address)
		} else {
			item.Address = "<hidden>"
		}
		result.Servers = append(result.Servers, item)
	}
	return result, nil
}

func validateDNSAddress(value string) error {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n\t ") {
		return errors.New("invalid DNS server address")
	}
	return nil // The installed Xray core validates supported DNS address schemes.
}

func ensureDNSServerNotReferenced(root, config map[string]any, servers []any, index int) error {
	if script, ok := config["script"].(string); ok && script != "" {
		return errors.New("DNS server references in a DNS script cannot be checked")
	}
	server, _ := getMap(servers[index])
	tag, _ := server["tag"].(string)
	if tag == "" {
		return nil
	}
	for key, value := range root {
		if key != "dns" && containsNetworkReference(value, tag) {
			return errors.New("DNS server is referenced by the Xray configuration")
		}
	}
	for key, value := range config {
		if key != "servers" && containsNetworkReference(value, tag) {
			return errors.New("DNS server is referenced by DNS configuration")
		}
	}
	for i, value := range servers {
		if i != index && containsNetworkReference(value, tag) {
			return errors.New("DNS server is referenced by another DNS server")
		}
	}
	return nil
}

func applyDNSChange(root map[string]any, change dnsChange) (string, error) {
	etag, err := dnsETag(root)
	if err != nil {
		return "", err
	}
	if change.ETag == "" || etag != change.ETag {
		return "", errDNSChanged
	}
	config, err := readDNS(root)
	if err != nil {
		return "", err
	}
	if config == nil {
		if change.Kind != "add" {
			return "", errors.New("configure a DNS server first")
		}
		config = map[string]any{}
	}
	servers, err := dnsServers(config)
	if err != nil {
		return "", err
	}
	script, _ := config["script"].(string)
	if script != "" && (change.Kind == "strategy" || change.Kind == "flag" || change.Kind == "move") {
		return "", errors.New("DNS strategy and flags are controlled by a DNS script")
	}
	switch change.Kind {
	case "add":
		if err := validateDNSAddress(change.Value); err != nil {
			return "", err
		}
		config["servers"] = append(servers, change.Value)
	case "update", "delete", "move":
		index := change.Index - 1
		if index < 0 || index >= len(servers) {
			return "", errDNSChanged
		}
		if change.Kind == "move" {
			delta := 0
			if change.Value == "up" {
				delta = -1
			} else if change.Value == "down" {
				delta = 1
			} else {
				return "", errors.New("direction must be up or down")
			}
			next := index + delta
			if next < 0 || next >= len(servers) {
				return "", errors.New("DNS server is already at the edge")
			}
			servers[index], servers[next] = servers[next], servers[index]
			config["servers"] = servers
			break
		}
		if change.Kind == "delete" {
			if err := ensureDNSServerNotReferenced(root, config, servers, index); err != nil {
				return "", err
			}
			config["servers"] = append(servers[:index:index], servers[index+1:]...)
			break
		}
		if err := validateDNSAddress(change.Value); err != nil {
			return "", err
		}
		switch server := servers[index].(type) {
		case string:
			if server == change.Value {
				return "", errors.New("DNS address is unchanged")
			}
			servers[index] = change.Value
		case map[string]any:
			if server["address"] == change.Value {
				return "", errors.New("DNS address is unchanged")
			}
			server["address"] = change.Value
		default:
			return "", errors.New("DNS server cannot be edited individually")
		}
		config["servers"] = servers
	case "strategy":
		switch change.Value {
		case "", "UseIP", "UseIPv4", "UseIPv6", "UseSystem":
		default:
			return "", errors.New("unknown DNS query strategy")
		}
		previous, _ := config["queryStrategy"].(string)
		if previous == change.Value {
			return "", errors.New("DNS strategy is unchanged")
		}
		if change.Value == "" {
			delete(config, "queryStrategy")
		} else {
			config["queryStrategy"] = change.Value
		}
	case "flag":
		if !editableDNSFlags[change.Field] {
			return "", errors.New("unknown DNS switch")
		}
		previous, _ := config[change.Field].(bool)
		if previous == change.Switch {
			return "", errors.New("DNS switch is unchanged")
		}
		config[change.Field] = change.Switch
	default:
		return "", errors.New("unknown DNS operation")
	}
	root["dns"] = config
	return fmt.Sprintf("Updated DNS (%s)", change.Kind), nil
}

func changeDNSConfig(cfg *BotConfig, change dnsChange) (string, error) {
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) { return applyDNSChange(root, change) })
	return message, sanitizeNetworkChangeError(err)
}
