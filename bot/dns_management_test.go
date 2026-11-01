package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDNSViewRedactsCredentialsAndPreservesServerSettings(t *testing.T) {
	root := agentFixtureRoot(t)
	root["dns"] = map[string]any{"servers": []any{
		"https://user:password@dns.example/dns-query?token=secret",
		map[string]any{"address": "1.1.1.1", "domains": []any{"domain:example.com"}, "custom": "preserved"},
	}, "queryStrategy": "UseIP"}
	view, err := dnsView(root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if bytes.Contains(encoded, []byte("password")) || bytes.Contains(encoded, []byte("token=secret")) || !bytes.Contains(encoded, []byte("dns.example")) {
		t.Fatalf("DNS view exposed credentials: %s", encoded)
	}
	change := dnsChange{Kind: "update", ETag: view.ETag, Index: 2, Value: "8.8.8.8"}
	if _, err = applyDNSChange(root, change); err != nil {
		t.Fatal(err)
	}
	if _, err = applyDNSChange(root, change); !errors.Is(err, errDNSChanged) {
		t.Fatalf("stale edit accepted: %v", err)
	}
	config, _ := readDNS(root)
	servers, _ := dnsServers(config)
	server := servers[1].(map[string]any)
	if server["custom"] != "preserved" || server["address"] != "8.8.8.8" || server["domains"].([]any)[0] != "domain:example.com" {
		t.Fatalf("lost server settings: %#v", server)
	}
}

func TestDNSReferenceAndScriptProtection(t *testing.T) {
	root := agentFixtureRoot(t)
	root["dns"] = map[string]any{"servers": []any{map[string]any{"address": "1.1.1.1", "tag": "dns-in"}}}
	root["routing"].(map[string]any)["rules"] = []any{map[string]any{"inboundTag": []any{"dns-in"}, "outboundTag": "direct"}}
	view, _ := dnsView(root)
	if _, err := applyDNSChange(root, dnsChange{Kind: "delete", ETag: view.ETag, Index: 1}); err == nil {
		t.Fatal("deleted referenced DNS server")
	}
	root["routing"].(map[string]any)["rules"] = []any{}
	root["dns"].(map[string]any)["script"] = "dns.lua"
	view, _ = dnsView(root)
	if !view.ScriptConfigured {
		t.Fatal("DNS script not reported")
	}
	if _, err := applyDNSChange(root, dnsChange{Kind: "delete", ETag: view.ETag, Index: 1}); err == nil {
		t.Fatal("deleted DNS server referenced by unknown script")
	}
	if _, err := applyDNSChange(root, dnsChange{Kind: "strategy", ETag: view.ETag, Value: "UseIPv4"}); err == nil {
		t.Fatal("changed ineffective strategy in script mode")
	}
}

func TestDNSMovesExactServerAndRejectsStaleList(t *testing.T) {
	root := agentFixtureRoot(t)
	root["dns"] = map[string]any{"servers": []any{"1.1.1.1", "8.8.8.8"}}
	view, _ := dnsView(root)
	change := dnsChange{Kind: "move", ETag: view.ETag, Index: 2, Value: "up"}
	if _, err := applyDNSChange(root, change); err != nil {
		t.Fatal(err)
	}
	if _, err := applyDNSChange(root, change); !errors.Is(err, errDNSChanged) {
		t.Fatalf("stale move accepted: %v", err)
	}
	config, _ := readDNS(root)
	servers, _ := dnsServers(config)
	if servers[0] != "8.8.8.8" || servers[1] != "1.1.1.1" {
		t.Fatalf("wrong server order: %#v", servers)
	}
}

func TestDNSStrategyAndSwitchChanges(t *testing.T) {
	root := agentFixtureRoot(t)
	root["dns"] = map[string]any{"servers": []any{"1.1.1.1"}, "hosts": map[string]any{"example.com": "192.0.2.1"}}
	view, _ := dnsView(root)
	if _, err := applyDNSChange(root, dnsChange{Kind: "flag", ETag: view.ETag, Field: "disableCache", Switch: true}); err != nil {
		t.Fatal(err)
	}
	view, _ = dnsView(root)
	if !view.Flags["disableCache"] {
		t.Fatal("cache switch was not updated")
	}
	if _, err := applyDNSChange(root, dnsChange{Kind: "strategy", ETag: view.ETag, Value: "UseIPv4"}); err != nil {
		t.Fatal(err)
	}
	view, _ = dnsView(root)
	if view.QueryStrategy != "UseIPv4" || root["dns"].(map[string]any)["hosts"] == nil {
		t.Fatal("strategy update lost DNS settings")
	}
	if _, err := applyDNSChange(root, dnsChange{Kind: "flag", ETag: view.ETag, Field: "unexpected", Switch: true}); err == nil {
		t.Fatal("accepted unknown DNS flag")
	}
}

func TestAgentDNSPreviewAndRealXray(t *testing.T) {
	root := agentFixtureRoot(t)
	root["dns"] = map[string]any{"servers": []any{"localhost"}}
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	view, _ := dnsView(root)
	op, _ := findAgentOperation("dns.server.add")
	input, _ := json.Marshal(map[string]any{"etag": view.ETag, "value": "1.1.1.1"})
	before, _ := os.ReadFile(h.config)
	if _, err := executeAgentOperation(cfg, op, input, false, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("preview changed DNS")
	}
	if _, err := executeAgentOperation(cfg, op, input, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAgentOperation(cfg, op, input, true, false); !errors.Is(err, errDNSChanged) {
		t.Fatalf("accepted stale DNS edit: %v", err)
	}
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	if output, err := exec.Command(bin, "run", "-test", "-config", h.config).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected DNS: %v\n%s", err, strings.TrimSpace(string(output)))
	}
}
