package main

import (
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"
)

func sectionSummaryField(object map[string]any, name string) (any, bool) {
	var result any
	found := false
	for key, value := range object {
		if strings.EqualFold(key, name) {
			if found {
				return nil, false
			}
			result, found = value, true
		}
	}
	return result, found
}

func sectionSummaryNumber(value any) any {
	switch number := value.(type) {
	case json.Number:
		if _, err := number.Int64(); err == nil {
			return number
		}
	case float64:
		if number == float64(int64(number)) {
			return number
		}
	case int:
		return number
	}
	return "<hidden>"
}

func policySummaryFields(object map[string]any, numeric, flags []string) map[string]any {
	result := map[string]any{}
	for _, name := range numeric {
		if value, ok := sectionSummaryField(object, name); ok {
			result[name] = sectionSummaryNumber(value)
		}
	}
	for _, name := range flags {
		if value, ok := sectionSummaryField(object, name); ok {
			if flag, ok := value.(bool); ok {
				result[name] = flag
			} else {
				result[name] = "<hidden>"
			}
		}
	}
	return result
}

// Only native numeric fields, known enums and CIDRs reach the ordinary UI.
// File paths, unknown extensions and malformed string values remain private.
func xraySectionSafeSummary(name string, value any) map[string]any {
	object, _ := getMap(value)
	result := map[string]any{}
	switch name {
	case "log":
		for _, field := range []string{"access", "error"} {
			mode := "stdout"
			if raw, ok := sectionSummaryField(object, field); ok {
				if text, ok := raw.(string); ok {
					if text == "none" {
						mode = "none"
					} else if text != "" {
						mode = "file"
					}
				} else {
					mode = "<hidden>"
				}
			}
			result[field] = mode
		}
		for field, allowed := range map[string][]string{"loglevel": {"debug", "info", "warning", "error", "none"}, "maskAddress": {"", "quarter", "half", "full"}} {
			if raw, ok := sectionSummaryField(object, field); ok {
				result[field] = "<hidden>"
				for _, candidate := range allowed {
					if raw == candidate {
						result[field] = candidate
					}
				}
			}
		}
		if raw, ok := sectionSummaryField(object, "dnsLog"); ok {
			if flag, ok := raw.(bool); ok {
				result["dnsLog"] = flag
			}
		}
	case "policy":
		if raw, ok := sectionSummaryField(object, "system"); ok {
			fields, _ := getMap(raw)
			result["system"] = policySummaryFields(fields, nil, []string{"statsInboundUplink", "statsInboundDownlink", "statsOutboundUplink", "statsOutboundDownlink"})
		}
		if raw, ok := sectionSummaryField(object, "levels"); ok {
			levels, _ := getMap(raw)
			var names []string
			for name := range levels {
				if _, err := strconv.ParseUint(name, 10, 32); err == nil {
					names = append(names, name)
				}
			}
			sort.Slice(names, func(i, j int) bool { return lessLevelName(names[i], names[j]) })
			if len(names) > 3 {
				result["additional_levels"] = len(names) - 3
				names = names[:3]
			}
			summaries := map[string]any{}
			for _, level := range names {
				fields, _ := getMap(levels[level])
				summaries[level] = policySummaryFields(fields, []string{"handshake", "connIdle", "uplinkOnly", "downlinkOnly", "bufferSize"}, []string{"statsUserUplink", "statsUserDownlink", "statsUserOnline"})
			}
			result["levels"] = summaries
		}
	case "fakedns":
		pools, ok := value.([]any)
		if !ok && object != nil {
			pools = []any{object}
		}
		if len(pools) > 3 {
			result["additional_pools"] = len(pools) - 3
			pools = pools[:3]
		}
		summaries := make([]map[string]any, 0, len(pools))
		for _, raw := range pools {
			pool, _ := getMap(raw)
			summary := map[string]any{}
			if raw, ok := sectionSummaryField(pool, "ipPool"); ok {
				text, _ := raw.(string)
				summary["ipPool"] = "<hidden>"
				if _, cidr, err := net.ParseCIDR(text); err == nil {
					summary["ipPool"] = cidr.String()
				}
			}
			if raw, ok := sectionSummaryField(pool, "poolSize"); ok {
				summary["poolSize"] = sectionSummaryNumber(raw)
			}
			summaries = append(summaries, summary)
		}
		result["pools"] = summaries
	}
	return result
}
