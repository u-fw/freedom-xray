package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type dnsHostRecord struct {
	Index    int      `json:"index"`
	Name     string   `json:"name"`
	Targets  []string `json:"targets"`
	Editable bool     `json:"editable"`
}

type dnsHostsSummary struct {
	ETag  string          `json:"etag"`
	Hosts []dnsHostRecord `json:"hosts"`
}

type dnsHostChange struct {
	Kind    string
	ETag    string
	Name    string
	NewName string
	Targets []string
}

func readDNSHosts(config map[string]any) (map[string]any, error) {
	if config == nil || config["hosts"] == nil {
		return map[string]any{}, nil
	}
	hosts, ok := getMap(config["hosts"])
	if !ok {
		return nil, errors.New("dns.hosts must be an object")
	}
	return hosts, nil
}

func validateDNSHostName(name string) error {
	if name == "" || len(name) > 1024 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\x00\r\n") {
		return errors.New("invalid DNS host match")
	}
	return nil // The installed Xray core validates domain match syntax.
}

func safeDNSHostName(name string) bool {
	if validateDNSHostName(name) != nil || strings.Contains(name, "://") {
		return false
	}
	if !strings.Contains(name, "@") {
		return true
	}
	if !strings.HasPrefix(strings.ToLower(name), "geosite:") {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(":@-_.", r) {
			return false
		}
	}
	return true
}

func validateDNSHostTargets(targets []string) error {
	if len(targets) == 0 || len(targets) > 128 {
		return errors.New("provide between 1 and 128 host targets")
	}
	for _, target := range targets {
		if target == "" || len(target) > 253 || strings.TrimSpace(target) != target || strings.ContainsAny(target, "\x00\r\n\t /@?[]") {
			return errors.New("invalid DNS host target")
		}
		if net.ParseIP(target) != nil {
			continue
		}
		if len(targets) > 1 {
			return errors.New("multiple host targets must all be IP addresses")
		}
		if strings.HasPrefix(target, "#") {
			if _, err := strconv.ParseUint(target[1:], 10, 8); err != nil {
				return errors.New("invalid DNS response code")
			}
			continue
		}
		if strings.ContainsAny(target, ":#") {
			return errors.New("host target must be an IP, domain, or # response code")
		}
	}
	return nil
}

func dnsHostValues(value any) ([]string, bool) {
	if target, ok := value.(string); ok {
		return []string{target}, true
	}
	if targets, ok := value.([]string); ok {
		return append([]string(nil), targets...), true
	}
	if raw, ok := value.([]any); ok {
		values := make([]string, 0, len(raw))
		for _, item := range raw {
			target, ok := item.(string)
			if !ok {
				return nil, false
			}
			values = append(values, target)
		}
		return values, true
	}
	return nil, false
}

func dnsHostNames(hosts map[string]any) []string {
	names := make([]string, 0, len(hosts))
	for name := range hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func dnsHostsView(root map[string]any) (dnsHostsSummary, error) {
	config, err := readDNS(root)
	if err != nil {
		return dnsHostsSummary{}, err
	}
	hosts, err := readDNSHosts(config)
	if err != nil {
		return dnsHostsSummary{}, err
	}
	etag, err := dnsETag(root)
	if err != nil {
		return dnsHostsSummary{}, err
	}
	result := dnsHostsSummary{ETag: etag, Hosts: make([]dnsHostRecord, 0, len(hosts))}
	for index, name := range dnsHostNames(hosts) {
		targets, valid := dnsHostValues(hosts[name])
		valid = valid && validateDNSHostTargets(targets) == nil
		safeName := name
		if !safeDNSHostName(name) {
			safeName, valid = "<hidden>", false
		}
		if !valid {
			targets = []string{"<hidden>"}
		}
		result.Hosts = append(result.Hosts, dnsHostRecord{Index: index + 1, Name: safeName, Targets: targets, Editable: valid})
	}
	return result, nil
}

func applyDNSHostChange(root map[string]any, change dnsHostChange) (string, error) {
	etag, err := dnsETag(root)
	if err != nil {
		return "", err
	}
	if change.ETag == "" || etag != change.ETag {
		return "", errDNSChanged
	}
	if err := validateDNSHostName(change.Name); err != nil {
		return "", err
	}
	config, err := readDNS(root)
	if err != nil {
		return "", err
	}
	if config == nil {
		if change.Kind != "add" {
			return "", errors.New("DNS host does not exist")
		}
		config = map[string]any{}
	}
	hosts, err := readDNSHosts(config)
	if err != nil {
		return "", err
	}
	old, exists := hosts[change.Name]
	switch change.Kind {
	case "add", "update":
		if change.Kind == "add" && exists {
			return "", errors.New("DNS host already exists")
		}
		if change.Kind == "update" && !exists {
			return "", errDNSChanged
		}
		if err := validateDNSHostTargets(change.Targets); err != nil {
			return "", err
		}
		var value any = change.Targets[0]
		if len(change.Targets) > 1 {
			value = append([]string(nil), change.Targets...)
		}
		before, _ := json.Marshal(old)
		after, _ := json.Marshal(value)
		if exists && string(before) == string(after) {
			return "", errors.New("DNS host is unchanged")
		}
		hosts[change.Name] = value
	case "rename":
		if !exists {
			return "", errDNSChanged
		}
		if err := validateDNSHostName(change.NewName); err != nil {
			return "", err
		}
		if _, taken := hosts[change.NewName]; taken {
			return "", errors.New("DNS host already exists")
		}
		hosts[change.NewName] = old
		delete(hosts, change.Name)
	case "delete":
		if !exists {
			return "", errDNSChanged
		}
		delete(hosts, change.Name)
	default:
		return "", errors.New("unknown DNS host operation")
	}
	config["hosts"] = hosts
	root["dns"] = config
	return fmt.Sprintf("Updated DNS host (%s)", change.Kind), nil
}

func changeDNSHostConfig(cfg *BotConfig, change dnsHostChange) (string, error) {
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) {
		return applyDNSHostChange(root, change)
	})
	return message, sanitizeNetworkChangeError(err)
}

type agentDNSHostInput struct {
	ETag    string   `json:"etag"`
	Name    string   `json:"name"`
	NewName string   `json:"new_name,omitempty"`
	Targets []string `json:"targets,omitempty"`
}

func agentDNSHostChange(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentDNSHostInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	if input.ETag == "" || input.Name == "" {
		return nil, errors.New("DNS etag and host name are required")
	}
	change := dnsHostChange{ETag: input.ETag, Name: input.Name, NewName: input.NewName, Targets: input.Targets}
	switch operation {
	case "dns.host.add", "dns.host.update":
		if input.NewName != "" || len(input.Targets) == 0 {
			return nil, errors.New("host add/update requires targets")
		}
		change.Kind = "add"
		if operation == "dns.host.update" {
			change.Kind = "update"
		}
	case "dns.host.rename":
		if input.NewName == "" || len(input.Targets) != 0 {
			return nil, errors.New("host rename requires a new_name")
		}
		change.Kind = "rename"
	case "dns.host.remove":
		if input.NewName != "" || len(input.Targets) != 0 {
			return nil, errors.New("host remove accepts only etag and name")
		}
		change.Kind = "delete"
	default:
		return nil, errors.New("unknown DNS host operation")
	}
	if !apply {
		root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
		if err != nil {
			return nil, err
		}
		message, err := applyDNSHostChange(root, change)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, message), nil
	}
	return agentMessage(changeDNSHostConfig(cfg, change))
}
