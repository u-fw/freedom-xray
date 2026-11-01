package main

import (
	"errors"
	"strings"
)

func requireCanonicalAPIFields(object map[string]any, fields []string) error {
	for key := range object {
		for _, name := range fields {
			if key != name && strings.EqualFold(key, name) {
				return errors.New("API safety checks require canonical JSON field names; remove case aliases")
			}
		}
	}
	return nil
}

// Safety checks must see every field Xray's case-insensitive struct decoder
// sees. Refuse aliases here rather than rewriting a partially managed config.
func validateAPIFieldCasing(root map[string]any) error {
	if err := requireCanonicalAPIFields(root, []string{"api", "inbounds", "outbounds", "routing"}); err != nil {
		return err
	}
	api, _ := getMap(root["api"])
	if err := requireCanonicalAPIFields(api, []string{"tag", "listen", "services"}); err != nil {
		return err
	}
	routing, _ := getMap(root["routing"])
	if err := requireCanonicalAPIFields(routing, []string{"rules", "balancers", "script"}); err != nil {
		return err
	}
	for _, spec := range []struct {
		Values any
		Fields []string
	}{
		{root["inbounds"], []string{"tag", "listen", "port", "protocol", "settings"}},
		{root["outbounds"], []string{"tag"}},
		{routing["rules"], []string{"type", "ruleTag", "inboundTag", "outboundTag", "balancerTag"}},
		{routing["balancers"], []string{"tag", "selector", "fallbackTag", "strategy"}},
	} {
		values, _ := spec.Values.([]any)
		for _, value := range values {
			object, _ := getMap(value)
			if err := requireCanonicalAPIFields(object, spec.Fields); err != nil {
				return err
			}
		}
	}
	inbounds, _ := root["inbounds"].([]any)
	for _, value := range inbounds {
		inbound, _ := getMap(value)
		if inbound["tag"] == "api-internal" {
			settings, _ := getMap(inbound["settings"])
			if err := requireCanonicalAPIFields(settings, []string{"address"}); err != nil {
				return err
			}
		}
	}
	return nil
}
