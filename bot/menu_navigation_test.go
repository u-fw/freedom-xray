package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNodeMenusPaginateAndGroupSettings(t *testing.T) {
	root := editableRoutingRoot(t)
	var inbounds []any
	for i := 0; i < 11; i++ {
		inbounds = append(inbounds, map[string]any{
			"tag": fmt.Sprintf("node-%02d", i), "port": 20000 + i, "protocol": "vless",
			"settings":       map[string]any{"decryption": "none", "clients": []any{}},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		})
	}
	root["inbounds"] = inbounds
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)

	s.sendNodesCardPage(9481, 1, 0)
	body, markup := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(body, "node-00") || strings.Contains(body, "node-08") || !strings.Contains(callbacks, "nodes_page:8") {
		t.Fatalf("first page incorrect: %s %s", body, callbacks)
	}
	s.sendNodesCardPage(9481, 1, 8)
	body, markup = capture.card(t)
	callbacks = strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(body, "node-08") || strings.Contains(body, "node-00") || !strings.Contains(callbacks, "nodes_page:0") {
		t.Fatalf("second page incorrect: %s %s", body, callbacks)
	}
	key := userCallbackKey("node-08")
	s.sendNodeDetail(9481, 1, key)
	_, markup = capture.card(t)
	callbacks = strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(callbacks, "node_advanced:"+key) || !strings.Contains(callbacks, "nodes_page:8") || strings.Contains(callbacks, "node_delete:") {
		t.Fatalf("basic settings incorrect: %s", callbacks)
	}
	s.sendNodeDetailSection(9481, 1, key, true)
	_, markup = capture.card(t)
	callbacks = strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(callbacks, "node_basic:"+key) || !strings.Contains(callbacks, "node_delete:"+key) {
		t.Fatalf("advanced settings incorrect: %s", callbacks)
	}
	cfg.AllowMutations = false
	s.sendNodeDetail(9481, 1, key)
	_, markup = capture.card(t)
	callbacks = strings.Join(keyboardCallbacks(t, markup), " ")
	if strings.Contains(callbacks, "node_advanced:") || strings.Contains(callbacks, "node_listen_port:") {
		t.Fatalf("read-only details expose edits: %s", callbacks)
	}
}

func TestRoutingListPaginates(t *testing.T) {
	root := editableRoutingRoot(t)
	rules, _ := routingRules(root)
	for i := 0; i < 8; i++ {
		rules = append(rules, map[string]any{"type": "field", "ruleTag": fmt.Sprintf("extra-%d", i), "domain": []any{fmt.Sprintf("domain:test%d.example", i)}, "outboundTag": "direct"})
	}
	root["routing"].(map[string]any)["rules"] = rules
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	s, capture := newCapturedRoutingBot(t, cfg)
	s.sendRoutingPage(9482, 1, 0, "")
	_, markup := capture.card(t)
	if !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "rt:page:6") {
		t.Fatal("routing next page missing")
	}
	s.sendRoutingPage(9482, 1, 6, "")
	_, markup = capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(callbacks, "rt:page:0") || strings.Contains(callbacks, "rt:page:12") {
		t.Fatalf("routing second page incorrect: %s", callbacks)
	}
}
