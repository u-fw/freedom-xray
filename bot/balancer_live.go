package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var errRoutingServiceDisabled = errors.New("RoutingService is not enabled")

type balancerLiveStatus struct {
	Tag             string   `json:"tag"`
	Selected        []string `json:"selected"`
	Override        string   `json:"override,omitempty"`
	StateETag       string   `json:"state_etag"`
	OverrideTargets []string `json:"override_targets"`
}

type boundedCommandOutput struct {
	bytes.Buffer
	limit int
}

func (output *boundedCommandOutput) Write(data []byte) (int, error) {
	if len(data) > output.limit-output.Len() {
		return 0, errors.New("command output exceeds limit")
	}
	return output.Buffer.Write(data)
}

func routingServiceConfigured(root map[string]any) bool {
	api, _ := getMap(root["api"])
	for _, service := range stringList(api["services"]) {
		if strings.EqualFold(service, "RoutingService") {
			return true
		}
	}
	return false
}

func queryBalancerLive(cfg *BotConfig, tag string) (balancerLiveStatus, error) {
	root, err := decodeBindingMigrationConfig(cfg.XrayConfigPath)
	if err != nil {
		return balancerLiveStatus{}, err
	}
	return queryBalancerLiveFromRoot(cfg, root, tag)
}

func routingAPIAddress(cfg *BotConfig, root map[string]any) (string, error) {
	server := cfg.StatsAPIServer
	if server == "" {
		server = "127.0.0.1:10085"
	}
	if _, err := validateAPIAccess(root, server); err != nil {
		return "", fmt.Errorf("unsafe API configuration: %w", err)
	}
	if !routingServiceConfigured(root) {
		return "", errRoutingServiceDisabled
	}
	ip, port, err := parseLoopbackAPIAddress(server)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

func queryBalancerLiveFromRoot(cfg *BotConfig, root map[string]any, tag string) (balancerLiveStatus, error) {
	_, item, err := findBalancer(root, tag)
	if err != nil || item == nil {
		return balancerLiveStatus{}, errors.New("balancer not found")
	}
	address, err := routingAPIAddress(cfg, root)
	if err != nil {
		return balancerLiveStatus{}, err
	}
	targets, err := balancerOverrideTargets(root, item)
	if err != nil {
		return balancerLiveStatus{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cfg.XrayBinPath, "api", "bi", "-json", "-server", address, "-timeout", "3", "--", tag)
	output := &boundedCommandOutput{limit: 256 * 1024}
	command.Stdout, command.Stderr = output, io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return balancerLiveStatus{}, errors.New("balancer status request timed out")
		}
		return balancerLiveStatus{}, errors.New("cannot query live balancer status; check the Xray service and core version")
	}
	var response struct {
		Balancer *struct {
			Override *struct {
				Target string `json:"target"`
			} `json:"override"`
			PrincipleTarget *struct {
				Tag []string `json:"tag"`
			} `json:"principleTarget"`
		} `json:"balancer"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Balancer == nil {
		return balancerLiveStatus{}, errors.New("Xray returned an unsupported balancer status response")
	}
	result := balancerLiveStatus{Tag: tag, Selected: []string{}, OverrideTargets: targets}
	if response.Balancer.Override != nil {
		result.Override = response.Balancer.Override.Target
	}
	if response.Balancer.PrincipleTarget != nil {
		result.Selected = response.Balancer.PrincipleTarget.Tag
	}
	// Automatic strategy preferences can fluctuate. The review reference tracks
	// configuration and manual override, not the strategy's transient selection.
	result.StateETag, err = routingRuleETag(map[string]any{"config": root, "tag": tag, "override": result.Override})
	return result, err
}

func applyRoutingService(root map[string]any, server string) (bool, error) {
	changed, err := applyStatsInfrastructure(root, server)
	if err != nil {
		return false, err
	}
	api, _ := getMap(root["api"])
	services, _ := api["services"].([]any)
	if !routingServiceConfigured(root) {
		api["services"] = append(services, "RoutingService")
		changed = true
	}
	return changed, nil
}

func enableRoutingService(cfg *BotConfig) (string, error) {
	server := cfg.StatsAPIServer
	if server == "" {
		server = "127.0.0.1:10085"
	}
	message, err := commitManagedConfig(cfg.XrayConfigPath, cfg.XrayBinPath, server, func(root map[string]any) (string, error) {
		changed, err := applyRoutingService(root, server)
		if err != nil {
			return "", err
		}
		if !changed {
			return "", errNoConfigChanges
		}
		return "Routing diagnostics enabled", nil
	})
	if errors.Is(err, errNoConfigChanges) {
		return "Routing diagnostics already enabled", nil
	}
	return message, sanitizeNetworkChangeError(err)
}
