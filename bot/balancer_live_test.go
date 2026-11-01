package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestRoutingServiceRequiresSafeAPIAndIsIdempotent(t *testing.T) {
	root := agentFixtureRoot(t)
	changed, err := applyRoutingService(root, "127.0.0.1:10085")
	if err != nil || !changed || !routingServiceConfigured(root) {
		t.Fatalf("routing service setup failed: %v %v", changed, err)
	}
	changed, err = applyRoutingService(root, "127.0.0.1:10085")
	if err != nil || changed {
		t.Fatalf("routing service setup not idempotent: %v %v", changed, err)
	}
	unsafe := agentFixtureRoot(t)
	unsafe["api"] = map[string]any{"tag": "api", "listen": "0.0.0.0:10085"}
	if _, err := applyRoutingService(unsafe, "127.0.0.1:10085"); err == nil {
		t.Fatal("enabled a remotely reachable routing API")
	}
}

func TestBalancerLiveRequiresRoutingService(t *testing.T) {
	root := agentFixtureRoot(t)
	root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool", "selector": []any{"direct"}}}
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	if _, err := queryBalancerLive(cfg, "pool"); !errors.Is(err, errRoutingServiceDisabled) {
		t.Fatalf("did not report disabled service: %v", err)
	}
}

func TestAgentRoutingServiceEnablePreviewsAndApplies(t *testing.T) {
	root := agentFixtureRoot(t)
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	operation, _ := findAgentOperation("routing.api.enable")
	before, _ := os.ReadFile(h.config)
	if _, err := executeAgentOperation(cfg, operation, nil, false, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("preview changed the configuration")
	}
	if _, err := executeAgentOperation(cfg, operation, nil, true, false); err != nil {
		t.Fatal(err)
	}
	root, err := decodeBindingMigrationConfig(h.config)
	if err != nil || !routingServiceConfigured(root) || h.restartCount(t) != 1 {
		t.Fatalf("routing service not enabled: %v", err)
	}
}

func TestBalancerFallbackValidationWithRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	root := agentFixtureRoot(t)
	for _, strategy := range []string{"random", "roundRobin"} {
		t.Run(strategy, func(t *testing.T) {
			item := map[string]any{"tag": "pool", "selector": []any{"direct"}, "fallbackTag": "block", "strategy": map[string]any{"type": strategy}}
			if err := validateBalancer(root, item); err == nil {
				t.Fatal("accepted fallback without an observatory")
			}
			root["observatory"] = map[string]any{"subjectSelector": []any{"direct"}, "probeInterval": "1m"}
			defer delete(root, "observatory")
			if err := validateBalancer(root, item); err != nil {
				t.Fatalf("rejected fallback with observatory: %v", err)
			}
			root["routing"].(map[string]any)["balancers"] = []any{item}
			data, _ := json.Marshal(root)
			path := writeRoutingConfig(t, string(data))
			if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
				t.Fatalf("Xray rejected monitored fallback: %v\n%s", err, output)
			}
		})
	}
}

func TestBalancerLiveWithRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	freePort := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port
	}
	for _, mode := range []string{"direct", "inbound"} {
		t.Run(mode, func(t *testing.T) {
			apiAddress := fmt.Sprintf("127.0.0.1:%d", freePort())
			root := agentFixtureRoot(t)
			root["inbounds"].([]any)[0].(map[string]any)["port"] = json.Number(strconv.Itoa(freePort()))
			if mode == "direct" {
				root["api"] = map[string]any{"tag": "api", "listen": apiAddress}
			}
			root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"tag": "direct2", "protocol": "freedom"}, map[string]any{"tag": "-r", "protocol": "freedom"})
			root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool", "selector": []any{"direct", "-r"}, "strategy": map[string]any{"type": "random"}}}
			if _, err := applyRoutingService(root, apiAddress); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(root)
			path := writeRoutingConfig(t, string(data))
			cmd := exec.Command(bin, "run", "-config", path)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			deadline := time.Now().Add(5 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp", apiAddress, 100*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("API did not start: %v; %s", err, output.String())
				}
				time.Sleep(50 * time.Millisecond)
			}
			cfg := DefaultConfig()
			cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer = path, bin, apiAddress
			status, err := queryBalancerLive(cfg, "pool")
			if err != nil || len(status.Selected) != 3 || len(status.OverrideTargets) != 3 || len(status.StateETag) != 24 {
				t.Fatalf("incorrect live balancer status: %+v %v", status, err)
			}
			change := balancerOverrideChange{Tag: "pool", StateETag: status.StateETag, Target: "direct2"}
			if _, err := setBalancerOverride(cfg, change, false); err != nil {
				t.Fatal(err)
			}
			updated, err := setBalancerOverride(cfg, change, true)
			if err != nil || updated.Override != "direct2" {
				t.Fatalf("real override failed: %+v %v", updated, err)
			}
			// Another administrator changes the core directly. Its state must not
			// be overwritten using the earlier review reference.
			if out, err := exec.Command(bin, "api", "bo", "-server", apiAddress, "-b", "pool", "--", "direct").CombinedOutput(); err != nil {
				t.Fatalf("external API switch failed: %v %s", err, out)
			}
			if _, err := setBalancerOverride(cfg, balancerOverrideChange{Tag: "pool", StateETag: updated.StateETag, Target: "-r"}, true); !errors.Is(err, errBalancerLiveChanged) {
				t.Fatalf("overwrote an external runtime change: %v", err)
			}
			current, _ := queryBalancerLive(cfg, "pool")
			literal, err := setBalancerOverride(cfg, balancerOverrideChange{Tag: "pool", StateETag: current.StateETag, Target: "-r"}, true)
			if err != nil || literal.Override != "-r" {
				t.Fatalf("option-like target was not treated literally: %+v %v", literal, err)
			}
			restored, err := setBalancerOverride(cfg, balancerOverrideChange{Tag: "pool", StateETag: literal.StateETag, Target: ""}, true)
			if err != nil || restored.Override != "" {
				t.Fatalf("real restore failed: %+v %v", restored, err)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, data) {
				t.Fatal("runtime operations wrote the configuration")
			}
		})
	}
}
