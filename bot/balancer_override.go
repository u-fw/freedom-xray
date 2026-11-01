package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

var errBalancerLiveChanged = errors.New("balancer configuration or manual override changed; refresh live status")

type balancerOverrideChange struct {
	Tag       string `json:"tag"`
	StateETag string `json:"state_etag"`
	Target    string `json:"target"`
}

func balancerOverrideTargets(root, item map[string]any) ([]string, error) {
	if err := validateOutboundSelectors(root, item["selector"]); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	values, ok := root["outbounds"].([]any)
	if !ok {
		return nil, errors.New("outbounds must be an array")
	}
	for _, value := range values {
		outbound, ok := getMap(value)
		if !ok {
			return nil, errors.New("invalid outbound")
		}
		tag, ok := outbound["tag"].(string)
		if !ok && outbound["tag"] != nil {
			return nil, errors.New("invalid outbound tag")
		}
		if tag != "" && seen[tag] {
			return nil, errors.New("duplicate outbound tags prevent safe override selection")
		}
		seen[tag] = true
	}
	targets := balancerMatchedOutbounds(root, item)
	if targets == nil {
		targets = []string{}
	}
	return targets, nil
}

// Runtime changes share the file lock with service controls and config writes.
// Xray provides no compare-and-swap API: external API writers can still race.
// No configuration is written, no traffic is reset and no service is restarted.
func setBalancerOverride(cfg *BotConfig, change balancerOverrideChange, apply bool) (balancerLiveStatus, error) {
	if change.Tag == "" || len(change.StateETag) != 24 || len(change.Target) > 256 || strings.TrimSpace(change.Target) != change.Target || strings.ContainsAny(change.Target, "\x00\r\n") {
		return balancerLiveStatus{}, errors.New("provide tag, current state_etag and a valid target (empty restores automatic selection)")
	}
	lock, err := acquireConfigLock(cfg.XrayConfigPath, 10*time.Second)
	if err != nil {
		return balancerLiveStatus{}, err
	}
	defer releaseConfigLock(lock)
	root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
	if err != nil {
		return balancerLiveStatus{}, err
	}
	_, item, err := findBalancer(root, change.Tag)
	if err != nil {
		return balancerLiveStatus{}, err
	}
	if item == nil {
		return balancerLiveStatus{}, errBalancerLiveChanged
	}
	address, err := routingAPIAddress(cfg, root)
	if err != nil {
		return balancerLiveStatus{}, err
	}
	if change.Target != "" {
		targets, err := balancerOverrideTargets(root, item)
		if err != nil {
			return balancerLiveStatus{}, err
		}
		allowed := false
		for _, target := range targets {
			if target == change.Target && target != networkAPITag(root) {
				allowed = true
			}
		}
		if !allowed {
			return balancerLiveStatus{}, errors.New("target must be an existing, unprotected outbound matched by this balancer's selectors")
		}
	}
	before, err := queryBalancerLiveFromRoot(cfg, root, change.Tag)
	if err != nil {
		return balancerLiveStatus{}, err
	}
	if before.StateETag != change.StateETag {
		return balancerLiveStatus{}, errBalancerLiveChanged
	}
	if before.Override == change.Target {
		return balancerLiveStatus{}, errors.New("manual override is unchanged")
	}
	if !apply {
		return before, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := []string{"api", "bo", "-server", address, "-timeout", "3", "-b", change.Tag}
	if change.Target == "" {
		args = append(args, "-r")
	} else {
		args = append(args, "--", change.Target)
	}
	command := exec.CommandContext(ctx, cfg.XrayBinPath, args...)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return balancerLiveStatus{}, errors.New("override request failed or timed out; runtime state may have changed, refresh live status before retrying")
	}
	after, err := queryBalancerLiveFromRoot(cfg, root, change.Tag)
	if err != nil || after.Override != change.Target {
		return balancerLiveStatus{}, errors.New("override request was sent but its result could not be verified; refresh live status")
	}
	return after, nil
}

func agentBalancerOverride(cfg *BotConfig, raw []byte, apply bool) (any, error) {
	var input struct {
		Tag       string  `json:"tag"`
		StateETag string  `json:"state_etag"`
		Target    *string `json:"target"`
	}
	if err := decodeAgentInput(raw, &input); err != nil {
		return nil, err
	}
	if input.Target == nil {
		return nil, errors.New("target is required; use an empty string to restore automatic selection")
	}
	change := balancerOverrideChange{Tag: input.Tag, StateETag: input.StateETag, Target: *input.Target}
	status, err := setBalancerOverride(cfg, change, apply)
	if err != nil {
		return nil, err
	}
	if !apply {
		return agentPreview("balancers.override", map[string]any{"before": status, "target": change.Target, "runtime_only": true, "restarts_xray": false}), nil
	}
	return map[string]any{"applied": true, "runtime_only": true, "status": status}, nil
}
