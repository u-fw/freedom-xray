package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

var xraySectionNames = []string{"log", "policy", "fakedns"}

var errXraySectionChanged = errors.New("Xray section changed; refresh and review it")

type xraySectionRecord struct {
	Name       string `json:"name"`
	ETag       string `json:"etag"`
	Configured bool   `json:"configured"`
	Shape      string `json:"shape"`
}

type xraySectionChange struct {
	Kind  string
	Name  string
	ETag  string
	Value any
}

type agentXraySectionInput struct {
	Name  string          `json:"name"`
	ETag  string          `json:"etag"`
	Value json.RawMessage `json:"value,omitempty"`
}

func xraySectionIndex(name string) int {
	for i, supported := range xraySectionNames {
		if name == supported {
			return i + 1
		}
	}
	return 0
}

// Xray's struct decoder accepts case variants, including fakeDns/fakedns.
// Refuse ambiguous variants because map re-encoding could change precedence.
func readXraySection(root map[string]any, name string) (string, any, bool, error) {
	if xraySectionIndex(name) == 0 {
		return "", nil, false, errors.New("unsupported Xray section")
	}
	field := ""
	for key := range root {
		if strings.EqualFold(key, name) {
			if field != "" {
				return "", nil, false, errors.New("ambiguous Xray section keys; use one canonical field before editing")
			}
			field = key
		}
	}
	if field == "" {
		return name, nil, false, nil
	}
	return field, root[field], true, nil
}

func xraySectionInfo(root map[string]any, name string) (xraySectionRecord, error) {
	_, value, present, err := readXraySection(root, name)
	if err != nil {
		return xraySectionRecord{}, err
	}
	encoded, err := json.Marshal(map[string]any{"present": present, "value": value})
	if err != nil {
		return xraySectionRecord{}, err
	}
	digest := sha256.Sum256(encoded)
	shape := "absent"
	if present {
		switch value.(type) {
		case map[string]any:
			shape = "object"
		case []any:
			shape = "array"
		case nil:
			shape = "null"
		default:
			shape = "other"
		}
	}
	return xraySectionRecord{Name: name, ETag: hex.EncodeToString(digest[:12]), Configured: present, Shape: shape}, nil
}

func xraySectionRecords(root map[string]any) ([]xraySectionRecord, error) {
	records := make([]xraySectionRecord, 0, len(xraySectionNames))
	for _, name := range xraySectionNames {
		record, err := xraySectionInfo(root, name)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func lookupXraySection(root map[string]any, name, etag string) (any, bool, error) {
	record, err := xraySectionInfo(root, name)
	if err != nil {
		return nil, false, err
	}
	if len(etag) != 24 || record.ETag != etag {
		return nil, false, errXraySectionChanged
	}
	_, value, present, err := readXraySection(root, name)
	return value, present, err
}

func validatePolicyStatisticsFields(object map[string]any, flags []string) error {
	if err := normalizeNativeObjectFields(object, flags); err != nil {
		return err
	}
	for _, name := range flags {
		if object[name] != nil {
			if _, ok := object[name].(bool); !ok {
				return errors.New("policy statistics switches must be booleans")
			}
		}
	}
	return nil
}

func validateFakeDNSPool(pool map[string]any) error {
	if err := normalizeNativeObjectFields(pool, []string{"ipPool", "poolSize"}); err != nil {
		return err
	}
	text, ok := pool["ipPool"].(string)
	if !ok {
		return errors.New("FakeDNS requires an ipPool CIDR")
	}
	_, network, err := net.ParseCIDR(text)
	if err != nil {
		return errors.New("FakeDNS ipPool must be a CIDR")
	}
	number, ok := pool["poolSize"].(json.Number)
	if !ok {
		return errors.New("FakeDNS requires an explicit positive poolSize")
	}
	size, err := number.Int64()
	if err != nil || size <= 0 {
		return errors.New("FakeDNS poolSize must be a positive integer")
	}
	ones, bits := network.Mask.Size()
	if room := bits - ones; room < 63 && uint64(size) >= uint64(1)<<room {
		return errors.New("FakeDNS poolSize must be smaller than its subnet address count")
	}
	return nil
}

func decodeXraySectionValue(name string, raw []byte) (any, error) {
	if xraySectionIndex(name) == 0 {
		return nil, errors.New("unsupported Xray section")
	}
	if len(raw) > telegramJSONDocumentLimit {
		return nil, errors.New("JSON section exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value, extra any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("expected a native JSON section value")
	}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected exactly one JSON value")
	}
	if object, ok := getMap(value); ok && object != nil {
		if len(object) == 1 {
			for field := range object {
				if strings.EqualFold(field, name) {
					return nil, errors.New("supply the section value without wrapping it in the section name")
				}
			}
		}
		var canonical []string
		if name == "policy" {
			canonical = []string{"levels", "system"}
		}
		if err := normalizeNativeObjectFields(object, canonical); err != nil {
			return nil, err
		}
		if name == "policy" {
			for _, field := range []string{"levels", "system"} {
				if object[field] != nil {
					if _, ok := getMap(object[field]); !ok {
						return nil, errors.New("policy levels and system must be objects")
					}
				}
			}
			if levels, ok := getMap(object["levels"]); ok {
				normalized := map[string]any{}
				for key, raw := range levels {
					levelNumber, err := strconv.ParseUint(key, 10, 32)
					if err != nil {
						return nil, errors.New("policy levels must use unsigned 32-bit numeric keys")
					}
					level, ok := getMap(raw)
					if !ok || level == nil {
						return nil, errors.New("each policy level must be an object")
					}
					canonical := strconv.FormatUint(levelNumber, 10)
					if _, exists := normalized[canonical]; exists {
						return nil, errors.New("multiple policy keys refer to the same level")
					}
					if err := validatePolicyStatisticsFields(level, []string{"statsUserUplink", "statsUserDownlink", "statsUserOnline"}); err != nil {
						return nil, err
					}
					normalized[canonical] = level
				}
				object["levels"] = normalized
			}
			if system, ok := getMap(object["system"]); ok {
				if err := validatePolicyStatisticsFields(system, []string{"statsInboundUplink", "statsInboundDownlink", "statsOutboundUplink", "statsOutboundDownlink"}); err != nil {
					return nil, err
				}
			}
		}
		if name == "fakedns" {
			if err := validateFakeDNSPool(object); err != nil {
				return nil, err
			}
		}
		return value, nil
	}
	if name == "fakedns" {
		if pools, ok := value.([]any); ok && len(pools) > 0 {
			for _, pool := range pools {
				object, ok := getMap(pool)
				if !ok || object == nil {
					return nil, errors.New("each FakeDNS pool must be an object")
				}
				if err := validateFakeDNSPool(object); err != nil {
					return nil, err
				}
			}
			return value, nil
		}
		return nil, errors.New("FakeDNS must be an object or a non-empty array of pools")
	}
	return nil, errors.New("this Xray section must be an object")
}

func applyXraySectionChange(root map[string]any, change xraySectionChange) (map[string]any, error) {
	_, present, err := lookupXraySection(root, change.Name, change.ETag)
	if err != nil {
		return nil, err
	}
	field, _, _, _ := readXraySection(root, change.Name)
	preview := map[string]any{"section": change.Name, "action": change.Kind}
	switch change.Kind {
	case "put":
		encoded, err := json.Marshal(change.Value)
		if err != nil {
			return nil, errors.New("invalid JSON section")
		}
		value, err := decodeXraySectionValue(change.Name, encoded)
		if err != nil {
			return nil, err
		}
		delete(root, field)
		root[change.Name] = value
	case "reset":
		if !present && change.Name != "policy" {
			return nil, errNoConfigChanges
		}
		delete(root, field)
	default:
		return nil, errors.New("unknown Xray section operation")
	}
	if change.Name == "policy" {
		enableStatsPolicy(root)
		preview["required_statistics_retained"] = true
	}
	if change.Name == "fakedns" && change.Kind == "reset" {
		preview["dns_may_initialize_default_pools"] = true
	}
	return preview, nil
}

func changeXraySectionConfig(cfg *BotConfig, change xraySectionChange) (string, error) {
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) {
		_, err := applyXraySectionChange(root, change)
		return fmt.Sprintf("Updated Xray %s configuration", change.Name), err
	})
	return message, sanitizeNetworkChangeError(err)
}

func agentXraySection(cfg *BotConfig, operation string, raw []byte, apply bool) (any, error) {
	var input agentXraySectionInput
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
	if err != nil {
		return nil, err
	}
	if operation == "xray.section.get" || operation == "xray.section.inspect" {
		if len(input.Value) != 0 {
			return nil, errors.New("section export requires only name and etag")
		}
		value, present, err := lookupXraySection(root, input.Name, input.ETag)
		if operation == "xray.section.inspect" {
			return map[string]any{"name": input.Name, "etag": input.ETag, "configured": present, "summary": xraySectionSafeSummary(input.Name, value)}, err
		}
		return map[string]any{"name": input.Name, "etag": input.ETag, "configured": present, "value": value}, err
	}
	change := xraySectionChange{Name: input.Name, ETag: input.ETag}
	switch operation {
	case "xray.section.put":
		if len(input.Value) == 0 {
			return nil, errors.New("section value is required")
		}
		change.Kind = "put"
		change.Value, err = decodeXraySectionValue(input.Name, input.Value)
		if err != nil {
			return nil, err
		}
	case "xray.section.reset":
		if len(input.Value) != 0 {
			return nil, errors.New("reset does not accept a value")
		}
		change.Kind = "reset"
	default:
		return nil, errors.New("unknown Xray section operation")
	}
	if !apply {
		preview, err := applyXraySectionChange(root, change)
		if err != nil {
			return nil, err
		}
		return agentPreview(operation, preview), nil
	}
	return agentMessage(changeXraySectionConfig(cfg, change))
}
