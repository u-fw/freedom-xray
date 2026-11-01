package main

import (
	"errors"
	"strings"
)

// Xray's struct decoder accepts case-insensitive field names. Canonicalize the
// known struct fields before management checks and reject ambiguous aliases.
// Do not recurse into arbitrary maps: their keys may be case-sensitive data.
func normalizeNativeObjectFields(object map[string]any, canonical []string) error {
	seen := map[string]bool{}
	for key := range object {
		folded := strings.ToLower(key)
		if seen[folded] {
			return errors.New("ambiguous JSON field casing")
		}
		seen[folded] = true
	}
	for _, name := range canonical {
		for key, value := range object {
			if key != name && strings.EqualFold(key, name) {
				delete(object, key)
				object[name] = value
				break
			}
		}
	}
	return nil
}

func copyNativeObjectFields(object map[string]any, canonical []string) (map[string]any, error) {
	if object == nil {
		return nil, errors.New("configuration must be an object")
	}
	copy := make(map[string]any, len(object))
	for key, value := range object {
		copy[key] = value
	}
	if err := normalizeNativeObjectFields(copy, canonical); err != nil {
		return nil, err
	}
	return copy, nil
}

func normalizeBalancerObject(object map[string]any) (map[string]any, error) {
	copy, err := copyNativeObjectFields(object, []string{"tag", "selector", "fallbackTag", "strategy"})
	if err != nil {
		return nil, err
	}
	if strategy, ok := getMap(copy["strategy"]); ok {
		copy["strategy"], err = copyNativeObjectFields(strategy, []string{"type", "settings"})
	}
	return copy, err
}

func normalizeObservatoryObject(object map[string]any) (map[string]any, error) {
	copy, err := copyNativeObjectFields(object, []string{"subjectSelector", "probeURL", "probeInterval", "enableConcurrency", "pingConfig"})
	if err != nil {
		return nil, err
	}
	if ping, ok := getMap(copy["pingConfig"]); ok {
		copy["pingConfig"], err = copyNativeObjectFields(ping, []string{"destination", "connectivity", "interval", "sampling", "timeout", "httpMethod"})
	}
	return copy, err
}
