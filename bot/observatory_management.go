package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

var errObservatoryChanged = errors.New("observatory changed; refresh the list")

type observatoryRecord struct {
	Kind           string         `json:"kind"`
	Configured     bool           `json:"configured"`
	ETag           string         `json:"etag,omitempty"`
	Config         map[string]any `json:"config,omitempty"`
	RedactedFields []string       `json:"redacted_fields,omitempty"`
}

type observatoryChange struct {
	Kind      string
	Action    string
	ETag      string
	Selectors any
	Field     string
	Value     string
	Config    map[string]any
}

func validObservatoryKind(kind string) bool {
	return kind == "observatory" || kind == "burstObservatory"
}

func readObservatory(root map[string]any, kind string) (map[string]any, error) {
	if !validObservatoryKind(kind) {
		return nil, errors.New("unknown observatory type")
	}
	value := root[kind]
	if value == nil {
		return nil, nil
	}
	config, ok := getMap(value)
	if !ok {
		return nil, errors.New("observatory must be an object")
	}
	return config, nil
}

func observatoryRecords(root map[string]any, sensitive bool) ([]observatoryRecord, error) {
	var records []observatoryRecord
	for _, kind := range []string{"observatory", "burstObservatory"} {
		config, err := readObservatory(root, kind)
		if err != nil {
			return nil, err
		}
		record := observatoryRecord{Kind: kind, Configured: config != nil}
		if config != nil {
			record.ETag, err = routingRuleETag(config)
			if err != nil {
				return nil, err
			}
			if sensitive {
				record.Config = config
			} else {
				record.Config = map[string]any{}
				for key, value := range config {
					switch key {
					case "subjectSelector", "probeInterval", "enableConcurrency":
						record.Config[key] = value
					default:
						record.Config[key] = "<hidden>"
						record.RedactedFields = append(record.RedactedFields, key)
					}
				}
				sort.Strings(record.RedactedFields)
			}
		}
		records = append(records, record)
	}
	return records, nil
}

func applyObservatoryChange(root map[string]any, change observatoryChange) (string, error) {
	current, err := readObservatory(root, change.Kind)
	if err != nil {
		return "", err
	}
	if current != nil {
		etag, err := routingRuleETag(current)
		if err != nil {
			return "", err
		}
		if change.ETag == "" || etag != change.ETag {
			return "", errObservatoryChanged
		}
	} else if change.ETag != "" || change.Action != "put" {
		return "", errObservatoryChanged
	}
	otherKind := "observatory"
	if change.Kind == otherKind {
		otherKind = "burstObservatory"
	}
	other, err := readObservatory(root, otherKind)
	if err != nil {
		return "", err
	}
	if change.Action == "delete" {
		if other == nil {
			balancers, err := routingBalancers(root)
			if err != nil {
				return "", err
			}
			for _, value := range balancers {
				item, ok := getMap(value)
				if !ok {
					return "", errors.New("invalid balancer object")
				}
				item, err = normalizeBalancerObject(item)
				if err != nil {
					return "", err
				}
				strategy := strings.ToLower(balancerStrategy(item))
				fallback, _ := item["fallbackTag"].(string)
				if strategy == "leastping" || strategy == "leastload" || fallback != "" {
					return "", errors.New("a balancer still requires an observatory")
				}
			}
		}
		delete(root, change.Kind)
		return fmt.Sprintf("Deleted %s", change.Kind), nil
	}
	if current == nil && other != nil {
		return "", errors.New("remove the other observatory before creating this type")
	}
	var candidate map[string]any
	switch change.Action {
	case "put":
		candidate, err = normalizeObservatoryObject(change.Config)
		if err != nil {
			return "", err
		}
	case "selectors":
		candidate, err = normalizeObservatoryObject(current)
		if err != nil {
			return "", err
		}
		candidate["subjectSelector"] = change.Selectors
	case "field":
		candidate, err = normalizeObservatoryObject(current)
		if err != nil {
			return "", err
		}
		switch change.Field {
		case "interval":
			duration, err := time.ParseDuration(change.Value)
			if err != nil || duration <= 0 || (change.Kind == "burstObservatory" && duration < 10*time.Second) {
				return "", errors.New("invalid probe interval")
			}
		case "url":
			parsed, err := url.Parse(change.Value)
			if err != nil || len(change.Value) > 2048 || strings.ContainsAny(change.Value, "\x00\r\n") || parsed == nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" {
				return "", errors.New("invalid probe URL")
			}
		default:
			return "", errors.New("unsupported observatory field")
		}
		if change.Kind == "observatory" {
			if change.Field == "interval" {
				for key := range candidate {
					if strings.EqualFold(key, "probeInterval") {
						delete(candidate, key)
					}
				}
				candidate["probeInterval"] = change.Value
			} else {
				for key := range candidate {
					if strings.EqualFold(key, "probeURL") {
						delete(candidate, key)
					}
				}
				candidate["probeURL"] = change.Value
			}
		} else {
			previous, ok := getMap(candidate["pingConfig"])
			if !ok && candidate["pingConfig"] != nil {
				return "", errors.New("pingConfig must be an object")
			}
			ping := make(map[string]any, len(previous)+1)
			for key, value := range previous {
				ping[key] = value
			}
			if change.Field == "interval" {
				ping["interval"] = change.Value
			} else {
				ping["destination"] = change.Value
			}
			candidate["pingConfig"] = ping
		}
	default:
		return "", errors.New("unknown observatory operation")
	}
	if err := validateOutboundSelectors(root, candidate["subjectSelector"]); err != nil {
		return "", err
	}
	if current != nil {
		before, beforeErr := json.Marshal(current)
		after, afterErr := json.Marshal(candidate)
		if beforeErr != nil || afterErr != nil {
			return "", errors.New("cannot encode observatory")
		}
		if bytes.Equal(before, after) {
			return "", errors.New("observatory is unchanged")
		}
	}
	root[change.Kind] = candidate
	return fmt.Sprintf("Updated %s", change.Kind), nil
}

func changeObservatoryConfig(cfg *BotConfig, change observatoryChange) (string, error) {
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, func(root map[string]any) (string, error) { return applyObservatoryChange(root, change) })
	return message, sanitizeNetworkChangeError(err)
}
