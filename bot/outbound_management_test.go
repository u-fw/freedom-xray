package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func outboundFixture(t *testing.T) map[string]any {
	t.Helper()
	root := editableRoutingRoot(t)
	root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"tag": "proxy", "protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "old.example", "port": json.Number("443"), "users": []any{map[string]any{"id": "secret-id", "encryption": "none"}}}}}, "custom": "preserve"})
	return root
}

func TestOutboundEndpointPreservesCredentialsAndRejectsStale(t *testing.T) {
	root := outboundFixture(t)
	records, err := outboundRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	record := records[3]
	encoded, _ := json.Marshal(records)
	if bytes.Contains(encoded, []byte("secret-id")) || record.Endpoint.Address != "old.example" {
		t.Fatalf("unexpected outbound summary: %s", encoded)
	}
	change := outboundChange{Kind: "address", Index: record.Index, ETag: record.ETag, Value: "new.example"}
	if _, err = applyOutboundChange(root, change); err != nil {
		t.Fatal(err)
	}
	if _, err = applyOutboundChange(root, change); !errors.Is(err, errOutboundChanged) {
		t.Fatalf("stale edit accepted: %v", err)
	}
	values, _ := outboundValues(root)
	item := values[3].(map[string]any)
	encoded, _ = json.Marshal(item)
	if !bytes.Contains(encoded, []byte("secret-id")) || item["custom"] != "preserve" || outboundEndpointInfo(item).Address != "new.example" {
		t.Fatalf("endpoint edit lost data: %s", encoded)
	}
	settings := item["settings"].(map[string]any)
	settings["address"] = "flat.example"
	settings["port"] = json.Number("8443")
	if outboundEndpointInfo(item).Address != "flat.example" {
		t.Fatal("flat endpoint did not override legacy vnext")
	}
}

func TestOutboundDefaultAndDeleteGuard(t *testing.T) {
	root := outboundFixture(t)
	records, _ := outboundRecords(root)
	change := outboundChange{Kind: "default", Index: 4, ETag: records[3].ETag, ListETag: records[3].ListETag}
	values, _ := outboundValues(root)
	values[1].(map[string]any)["protocol"] = "freedom"
	if _, err := applyOutboundChange(root, change); !errors.Is(err, errOutboundChanged) {
		t.Fatalf("changed list accepted: %v", err)
	}
	records, _ = outboundRecords(root)
	root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool", "selector": []any{"prox"}}}
	if _, err := applyOutboundChange(root, outboundChange{Kind: "delete", Index: 4, ETag: records[3].ETag}); err == nil {
		t.Fatal("deleted outbound selected by balancer")
	}
	root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool", "selector": []any{"direct"}}}
	if _, err := applyOutboundChange(root, outboundChange{Kind: "delete", Index: 4, ETag: records[3].ETag}); err != nil {
		t.Fatalf("unrelated balancer blocked deletion: %v", err)
	}
	records, _ = outboundRecords(root)
	if _, err := applyOutboundChange(root, outboundChange{Kind: "delete", Index: 1, ETag: records[0].ETag}); err == nil {
		t.Fatal("deleted default outbound")
	}
}

func TestAgentOutboundPreviewAndRealXray(t *testing.T) {
	root := agentFixtureRoot(t)
	root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"tag": "proxy", "protocol": "vless", "settings": map[string]any{"address": "127.0.0.1", "port": json.Number("28443"), "id": "11111111-1111-4111-8111-111111111111", "encryption": "none"}})
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	records, _ := outboundRecords(root)
	input, _ := json.Marshal(map[string]any{"index": 3, "etag": records[2].ETag, "field": "port", "value": "28444"})
	op, _ := findAgentOperation("outbounds.endpoint")
	before, _ := os.ReadFile(h.config)
	if _, err := executeAgentOperation(cfg, op, input, false, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("preview wrote config")
	}
	if _, err := executeAgentOperation(cfg, op, input, true, false); err != nil {
		t.Fatal(err)
	}
	if h.restartCount(t) != 1 {
		t.Fatal("apply did not restart")
	}
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	if output, err := exec.Command(bin, "run", "-test", "-config", h.config).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected edited outbound: %v\n%s", err, output)
	}
}

func TestAgentOutboundCreateDefaultAndStaleList(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	root, _ := decodeBindingMigrationConfig(h.config)
	records, _ := outboundRecords(root)
	create, _ := findAgentOperation("outbounds.create")
	input, _ := json.Marshal(map[string]any{"tag": "new-block", "protocol": "blackhole", "list_etag": records[0].ListETag})
	if _, err := executeAgentOperation(cfg, create, input, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAgentOperation(cfg, create, input, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAgentOperation(cfg, create, input, true, false); !errors.Is(err, errOutboundChanged) {
		t.Fatalf("accepted stale outbound list: %v", err)
	}
	root, _ = decodeBindingMigrationConfig(h.config)
	records, _ = outboundRecords(root)
	setDefault, _ := findAgentOperation("outbounds.default")
	input, _ = json.Marshal(map[string]any{"index": 3, "etag": records[2].ETag, "list_etag": records[2].ListETag})
	if _, err := executeAgentOperation(cfg, setDefault, input, true, false); err != nil {
		t.Fatal(err)
	}
	root, _ = decodeBindingMigrationConfig(h.config)
	records, _ = outboundRecords(root)
	if records[0].Tag != "new-block" || h.restartCount(t) != 2 {
		t.Fatalf("default not changed: %#v", records)
	}
}
