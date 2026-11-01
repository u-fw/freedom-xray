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
			root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool", "selector": []any{"direct"}, "strategy": map[string]any{"type": "random"}}}
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
			if err != nil || len(status.Selected) != 1 || status.Selected[0] != "direct" {
				t.Fatalf("incorrect live balancer status: %+v %v", status, err)
			}
		})
	}
}
