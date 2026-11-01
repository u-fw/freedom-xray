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

var errOutboundChanged = errors.New("outbound changed; refresh the list")

type outboundEndpoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type outboundRecord struct {
	Index    int               `json:"index"`
	ETag     string            `json:"etag"`
	ListETag string            `json:"list_etag"`
	Tag      string            `json:"tag,omitempty"`
	Protocol string            `json:"protocol,omitempty"`
	Default  bool              `json:"default"`
	Endpoint *outboundEndpoint `json:"endpoint,omitempty"`
}

type outboundChange struct {
	Kind     string
	Index    int
	ETag     string
	ListETag string
	Tag      string
	Protocol string
	Value    string
}

func outboundValues(root map[string]any) ([]any, error) {
	values, ok := root["outbounds"].([]any)
	if !ok || len(values) == 0 {
		return nil, errors.New("at least one outbound is required")
	}
	return values, nil
}

func outboundListETag(root map[string]any) (string, error) {
	values, err := outboundValues(root)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:12]), nil
}

// Return only a uniquely identifiable server. Flat settings override legacy
// arrays in Xray; multi-server legacy layouts remain read-only here.
func outboundEndpointMap(item map[string]any) map[string]any {
	protocol, _ := item["protocol"].(string)
	var field string
	switch protocol {
	case "vless", "vmess":
		field = "vnext"
	case "trojan", "shadowsocks", "socks", "http":
		field = "servers"
	default:
		return nil
	}
	settings, ok := getMap(item["settings"])
	if !ok {
		return nil
	}
	if settings["address"] != nil {
		return settings
	}
	values, ok := settings[field].([]any)
	if !ok || len(values) != 1 {
		return nil
	}
	entry, _ := getMap(values[0])
	return entry
}

func outboundEndpointInfo(item map[string]any) *outboundEndpoint {
	entry := outboundEndpointMap(item)
	if entry == nil {
		return nil
	}
	address, ok := entry["address"].(string)
	if !ok {
		return nil
	}
	if _, err := normalizeOutboundAddress(address); err != nil {
		address = "<hidden>"
	}
	var port int
	switch value := entry["port"].(type) {
	case json.Number:
		port, _ = strconv.Atoi(value.String())
	case float64:
		port = int(value)
	case int:
		port = value
	}
	return &outboundEndpoint{Address: address, Port: port}
}

func outboundRecords(root map[string]any) ([]outboundRecord, error) {
	values, err := outboundValues(root)
	if err != nil {
		return nil, err
	}
	listETag, err := outboundListETag(root)
	if err != nil {
		return nil, err
	}
	records := make([]outboundRecord, 0, len(values))
	for index, value := range values {
		item, ok := getMap(value)
		if !ok {
			return nil, errors.New("invalid outbound object")
		}
		etag, err := routingRuleETag(item)
		if err != nil {
			return nil, err
		}
		tag, _ := item["tag"].(string)
		protocol, _ := item["protocol"].(string)
		records = append(records, outboundRecord{Index: index + 1, ETag: etag, ListETag: listETag, Tag: tag, Protocol: protocol, Default: index == 0, Endpoint: outboundEndpointInfo(item)})
	}
	return records, nil
}

func lookupOutbound(root map[string]any, index int, etag string) (map[string]any, error) {
	values, err := outboundValues(root)
	if err != nil {
		return nil, err
	}
	if index < 1 || index > len(values) || len(etag) != 24 {
		return nil, errOutboundChanged
	}
	item, ok := getMap(values[index-1])
	if !ok {
		return nil, errOutboundChanged
	}
	actual, err := routingRuleETag(item)
	if err != nil {
		return nil, err
	}
	if actual != etag {
		return nil, errOutboundChanged
	}
	return item, nil
}

func normalizeOutboundAddress(value string) (string, error) {
	host := strings.TrimSpace(value)
	if host == "" || host != value || len(host) > 253 || strings.ContainsAny(host, "\x00\r\n\t /@") {
		return "", errors.New("enter a hostname or IP address")
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return "", errors.New("invalid outbound address")
		}
		return host, nil
	}
	host = strings.TrimSuffix(host, ".")
	if !serverHostnamePattern.MatchString(host) {
		return "", errors.New("enter a hostname or IP address without a port")
	}
	return host, nil
}

func applyOutboundChange(root map[string]any, change outboundChange) (string, error) {
	values, err := outboundValues(root)
	if err != nil {
		return "", err
	}
	if change.Kind == "create" || change.Kind == "default" {
		current, err := outboundListETag(root)
		if err != nil {
			return "", err
		}
		if change.ListETag == "" || current != change.ListETag {
			return "", errOutboundChanged
		}
	}
	if change.Kind == "create" {
		if err := validateNodeTag(change.Tag); err != nil {
			return "", err
		}
		if change.Tag == networkAPITag(root) {
			return "", errors.New("the internal API outbound is protected")
		}
		if change.Protocol != "freedom" && change.Protocol != "blackhole" {
			return "", errors.New("unsupported guided outbound protocol")
		}
		for _, value := range values {
			item, ok := getMap(value)
			if !ok {
				return "", errors.New("invalid outbound object")
			}
			if item["tag"] == change.Tag {
				return "", errors.New("outbound tag already exists")
			}
		}
		root["outbounds"] = append(values, map[string]any{"tag": change.Tag, "protocol": change.Protocol})
		return fmt.Sprintf("Added outbound %s", change.Tag), nil
	}
	item, err := lookupOutbound(root, change.Index, change.ETag)
	if err != nil {
		return "", err
	}
	tag, _ := item["tag"].(string)
	if tag != "" && tag == networkAPITag(root) {
		return "", errors.New("the internal API outbound is protected")
	}
	index := change.Index - 1
	switch change.Kind {
	case "delete":
		if err := validateOutboundDeletion(root, values, index, tag); err != nil {
			return "", err
		}
		root["outbounds"] = append(values[:index:index], values[index+1:]...)
		return fmt.Sprintf("Deleted outbound %d", change.Index), nil
	case "default":
		if index == 0 {
			return "", errors.New("outbound is already the default")
		}
		ordered := make([]any, 0, len(values))
		ordered = append(ordered, values[index])
		ordered = append(ordered, values[:index]...)
		ordered = append(ordered, values[index+1:]...)
		root["outbounds"] = ordered
		return fmt.Sprintf("Set outbound %d as default", change.Index), nil
	case "address", "port":
		endpoint := outboundEndpointMap(item)
		if endpoint == nil {
			return "", errors.New("this outbound has no uniquely editable server")
		}
		if change.Kind == "address" {
			address, err := normalizeOutboundAddress(change.Value)
			if err != nil {
				return "", err
			}
			if endpoint["address"] == address {
				return "", errors.New("address is unchanged")
			}
			endpoint["address"] = address
		} else {
			port, err := strconv.Atoi(change.Value)
			if err != nil || port < 1 || port > 65535 {
				return "", errors.New("port must be between 1 and 65535")
			}
			info := outboundEndpointInfo(item)
			if info != nil && info.Port == port {
				return "", errors.New("port is unchanged")
			}
			endpoint["port"] = json.Number(strconv.Itoa(port))
		}
		return fmt.Sprintf("Updated outbound %d", change.Index), nil
	default:
		return "", errors.New("unknown outbound operation")
	}
}

func changeOutboundConfig(cfg *BotConfig, change outboundChange) (string, error) {
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) { return applyOutboundChange(root, change) })
	return message, sanitizeNetworkChangeError(err)
}
