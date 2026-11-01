package main

import "errors"

func lookupBalancer(root map[string]any, tag, etag string) (map[string]any, error) {
	_, item, err := findBalancer(root, tag)
	if err != nil {
		return nil, err
	}
	if item == nil || etag == "" {
		return nil, errBalancerChanged
	}
	actual, err := routingRuleETag(item)
	if err != nil {
		return nil, err
	}
	if actual != etag {
		return nil, errBalancerChanged
	}
	return item, nil
}

func observatoryIndex(kind string) int {
	if kind == "observatory" {
		return 1
	}
	if kind == "burstObservatory" {
		return 2
	}
	return 0
}

func observatoryKindAtIndex(index int) string {
	if index == 1 {
		return "observatory"
	}
	if index == 2 {
		return "burstObservatory"
	}
	return ""
}

func lookupObservatory(root map[string]any, kind, etag string) (map[string]any, error) {
	item, err := readObservatory(root, kind)
	if err != nil {
		return nil, err
	}
	if item == nil || etag == "" {
		return nil, errObservatoryChanged
	}
	actual, err := routingRuleETag(item)
	if err != nil {
		return nil, err
	}
	if actual != etag {
		return nil, errObservatoryChanged
	}
	return item, nil
}

func agentNativeBalancingGet(cfg *BotConfig, operation string, raw []byte) (any, error) {
	var input struct {
		Tag  string `json:"tag,omitempty"`
		Kind string `json:"kind,omitempty"`
		ETag string `json:"etag"`
	}
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
	if err != nil {
		return nil, err
	}
	if operation == "balancers.get" {
		if input.Tag == "" || input.Kind != "" || input.ETag == "" {
			return nil, errors.New("balancers.get requires tag and etag")
		}
		return lookupBalancer(root, input.Tag, input.ETag)
	}
	if !validObservatoryKind(input.Kind) || input.Tag != "" || input.ETag == "" {
		return nil, errors.New("observatories.get requires kind and etag")
	}
	return lookupObservatory(root, input.Kind, input.ETag)
}
