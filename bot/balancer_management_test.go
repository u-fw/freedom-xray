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

func testBalancer(tag string) map[string]any {
	return map[string]any{"tag": tag, "selector": []any{"direct"}, "strategy": map[string]any{"type": "random", "settings": map[string]any{"secret": "hidden-token"}}, "custom": "hidden-extension"}
}

func TestBalancerCRUDReferencesAndStaleEdits(t *testing.T) {
	root := editableRoutingRoot(t)
	if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: testBalancer("pool")}); err != nil {
		t.Fatal(err)
	}
	records, err := balancerRecords(root)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %#v %v", records, err)
	}
	ref := records[0].ETag
	if _, err := applyBalancerChange(root, balancerChange{Kind: "patch", Tag: "pool", ETag: ref, Field: "selector", Value: []any{"block"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyBalancerChange(root, balancerChange{Kind: "delete", Tag: "pool", ETag: ref}); !errors.Is(err, errBalancerChanged) {
		t.Fatalf("stale delete accepted: %v", err)
	}
	records, _ = balancerRecords(root)
	if records[0].Balancer["custom"] != "hidden-extension" {
		t.Fatal("patch lost an unknown field")
	}
	rules, _ := routingRules(root)
	rules[1].(map[string]any)["balancerTag"] = "pool"
	if _, err := applyBalancerChange(root, balancerChange{Kind: "delete", Tag: "pool", ETag: records[0].ETag}); err == nil {
		t.Fatal("deleted referenced balancer")
	}
	delete(rules[1].(map[string]any), "balancerTag")
	if _, err := applyBalancerChange(root, balancerChange{Kind: "delete", Tag: "pool", ETag: records[0].ETag}); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := routingBalancers(root); len(remaining) != 0 {
		t.Fatalf("balancer not deleted: %#v", remaining)
	}
}

func TestBalancerRequiresSafeSelectorsAndObservatory(t *testing.T) {
	root := editableRoutingRoot(t)
	api := testBalancer("unsafe")
	api["selector"] = []any{"a"}
	if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "unsafe", Balancer: api}); err == nil {
		t.Fatal("accepted selector that matches API tag")
	}
	monitored := testBalancer("monitored")
	monitored["strategy"] = map[string]any{"type": "leastPing"}
	if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "monitored", Balancer: monitored}); err == nil {
		t.Fatal("accepted monitor-based strategy without observatory")
	}
	root["observatory"] = map[string]any{"subjectSelector": []any{"direct"}}
	if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "monitored", Balancer: monitored}); err != nil {
		t.Fatal(err)
	}
}

func TestBalancerFallbackRequiresObservatory(t *testing.T) {
	for _, strategy := range []string{"random", "roundRobin"} {
		t.Run(strategy, func(t *testing.T) {
			root := editableRoutingRoot(t)
			item := map[string]any{"tag": "pool", "selector": []any{"direct"}, "fallbackTag": "block", "strategy": map[string]any{"type": strategy}}
			if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: item}); err == nil {
				t.Fatal("accepted fallback without an observatory")
			}
			root["observatory"] = map[string]any{"subjectSelector": []any{"direct"}}
			if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: item}); err != nil {
				t.Fatal(err)
			}
			records, _ := observatoryRecords(root, false)
			if _, err := applyObservatoryChange(root, observatoryChange{Kind: "observatory", Action: "delete", ETag: records[0].ETag}); err == nil {
				t.Fatal("deleted observatory needed by fallback")
			}
		})
	}
}

func TestAgentBalancerPreviewAndRedaction(t *testing.T) {
	root := editableRoutingRoot(t)
	if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: testBalancer("pool")}); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	list, _ := findAgentOperation("balancers.list")
	output, err := executeAgentOperation(cfg, list, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(output)
	if bytes.Contains(encoded, []byte("hidden-token")) || bytes.Contains(encoded, []byte("hidden-extension")) || !bytes.Contains(encoded, []byte("redacted_fields")) {
		t.Fatalf("sensitive fields exposed: %s", encoded)
	}
	output, err = executeAgentOperation(cfg, list, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(output)
	if !bytes.Contains(encoded, []byte("hidden-token")) {
		t.Fatal("sensitive opt-in lost native settings")
	}
	records, _ := balancerRecords(root)
	input, _ := json.Marshal(map[string]any{"tag": "pool", "etag": records[0].ETag, "field": "strategy", "value": "roundRobin"})
	operation, _ := findAgentOperation("balancers.patch")
	before, _ := os.ReadFile(h.config)
	if _, err = executeAgentOperation(cfg, operation, input, false, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("preview modified config")
	}
	if _, err = executeAgentOperation(cfg, operation, input, true, false); err != nil {
		t.Fatal(err)
	}
	if h.restartCount(t) != 1 {
		t.Fatal("apply did not restart")
	}
}

func TestBalancerAcceptedByRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	root := agentFixtureRoot(t)
	item := map[string]any{"tag": "pool", "selector": []any{"direct"}, "strategy": map[string]any{"type": "roundRobin"}}
	if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: item}); err != nil {
		t.Fatal(err)
	}
	routing := root["routing"].(map[string]any)
	routing["rules"] = []any{map[string]any{"type": "field", "domain": []any{"domain:example.com"}, "balancerTag": "pool"}}
	data, _ := json.Marshal(root)
	path := writeRoutingConfig(t, string(data))
	if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected balancer: %v\n%s", err, strings.TrimSpace(string(output)))
	}
}
