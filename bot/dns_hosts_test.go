package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestDNSHostCRUDAndStaleChanges(t *testing.T) {
	root := agentFixtureRoot(t)
	root["dns"] = map[string]any{"servers": []any{"localhost"}, "custom": "preserved", "hosts": map[string]any{"existing.test": "192.0.2.9"}}
	view, err := dnsHostsView(root)
	if err != nil {
		t.Fatal(err)
	}
	change := dnsHostChange{Kind: "add", ETag: view.ETag, Name: "domain:example.com", Targets: []string{"192.0.2.1", "2001:db8::1"}}
	if _, err := applyDNSHostChange(root, change); err != nil {
		t.Fatal(err)
	}
	if _, err := applyDNSHostChange(root, change); !errors.Is(err, errDNSChanged) {
		t.Fatalf("stale edit accepted: %v", err)
	}
	view, _ = dnsHostsView(root)
	if len(view.Hosts) != 2 || view.Hosts[0].Name != "domain:example.com" || len(view.Hosts[0].Targets) != 2 || !view.Hosts[0].Editable {
		t.Fatalf("unexpected host summary: %+v", view)
	}
	if _, err := applyDNSHostChange(root, dnsHostChange{Kind: "rename", ETag: view.ETag, Name: "domain:example.com", NewName: "full:example.com"}); err != nil {
		t.Fatal(err)
	}
	view, _ = dnsHostsView(root)
	if _, err := applyDNSHostChange(root, dnsHostChange{Kind: "update", ETag: view.ETag, Name: "full:example.com", Targets: []string{"alias.example"}}); err != nil {
		t.Fatal(err)
	}
	view, _ = dnsHostsView(root)
	if _, err := applyDNSHostChange(root, dnsHostChange{Kind: "delete", ETag: view.ETag, Name: "full:example.com"}); err != nil {
		t.Fatal(err)
	}
	config, _ := readDNS(root)
	hosts, _ := readDNSHosts(config)
	if len(hosts) != 1 || hosts["existing.test"] != "192.0.2.9" || config["custom"] != "preserved" || len(config["servers"].([]any)) != 1 {
		t.Fatalf("unrelated DNS settings changed: %#v", config)
	}
}

func TestDNSHostValidationAndRedaction(t *testing.T) {
	root := agentFixtureRoot(t)
	root["dns"] = map[string]any{"hosts": map[string]any{
		"bad.example":                        "https://user:secret@example.test/path",
		"https://user:password@example.test": "192.0.2.2",
		"geosite:example@cn":                 "192.0.2.3",
	}}
	view, err := dnsHostsView(root)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(view)
	if bytes.Contains(data, []byte("password")) || bytes.Contains(data, []byte("secret")) {
		t.Fatalf("host view leaked a malformed target or name: %s", data)
	}
	if !bytes.Contains(data, []byte("geosite:example@cn")) {
		t.Fatal("valid geosite attribute hidden from host view")
	}
	for _, targets := range [][]string{{}, {"192.0.2.1", "alias.example"}, {"192.0.2.1", "https://example.test/secret"}} {
		if _, err := applyDNSHostChange(root, dnsHostChange{Kind: "add", ETag: view.ETag, Name: "example.com", Targets: targets}); err == nil {
			t.Fatalf("invalid host targets accepted: %#v", targets)
		}
	}
	if _, err := applyDNSHostChange(root, dnsHostChange{Kind: "add", ETag: view.ETag, Name: "bad.example", Targets: []string{"192.0.2.1"}}); err == nil {
		t.Fatal("duplicate DNS host accepted")
	}
}

func TestAgentDNSHostPreviewAndRealXray(t *testing.T) {
	root := agentFixtureRoot(t)
	delete(root, "dns")
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	view, _ := dnsHostsView(root)
	op, _ := findAgentOperation("dns.host.add")
	input, _ := json.Marshal(map[string]any{"etag": view.ETag, "name": "full:example.com", "targets": []string{"192.0.2.1", "2001:db8::1"}})
	before, _ := os.ReadFile(h.config)
	if _, err := executeAgentOperation(cfg, op, input, false, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("preview changed DNS hosts")
	}
	if _, err := executeAgentOperation(cfg, op, input, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAgentOperation(cfg, op, input, true, false); !errors.Is(err, errDNSChanged) {
		t.Fatalf("stale host edit accepted: %v", err)
	}
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	if output, err := exec.Command(bin, "run", "-test", "-config", h.config).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected DNS hosts: %v\n%s", err, output)
	}
}

func TestDNSHostBotGuidedCreateAndStaleEdit(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9531, Type: "private"}, &tgbotapi.User{ID: 17}
	root, _ := decodeBindingMigrationConfig(h.config)
	view, _ := dnsHostsView(root)
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "dh:add:" + view.ETag}
	s.handleDNSHostCallback(cb)
	if !s.handleDNSInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "domain:example.com"}) || !s.handleDNSInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "192.0.2.1\n2001:db8::1"}) {
		t.Fatal("guided host creation was not handled")
	}
	body, _ := capture.card(t)
	if !strings.Contains(body, "example.com") || !strings.Contains(body, "192.0.2.1") {
		t.Fatalf("confirmation omitted host details: %s", body)
	}
	before, _ := os.ReadFile(h.config)
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgbotapi.User{ID: 18}
	s.resolveRoutingChange(cb, true)
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("another admin confirmed host creation")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	view, _ = dnsHostsView(root)
	if len(view.Hosts) != 1 || view.Hosts[0].Name != "domain:example.com" || h.restartCount(t) != 1 {
		t.Fatalf("host mapping not created: %+v", view)
	}
	s.sendDNSHostDetail(chat.ID, 1, 1, view.ETag)
	body, markup := capture.card(t)
	if !strings.Contains(body, "2001:db8::1") || !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "dh:rename:") {
		t.Fatalf("incomplete host detail: %s", body)
	}
	cb.Data = dnsHostCallback("edit", 1, view.ETag)
	s.handleDNSHostCallback(cb)
	if !s.handleDNSInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "alias.example"}) {
		t.Fatal("host update not handled")
	}
	token := pendingRoutingToken(t, chat.ID)
	root["dns"].(map[string]any)["queryStrategy"] = "UseIPv4"
	data, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	cb.Data = "routing_confirm:" + token
	s.resolveRoutingChange(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(data, after) || h.restartCount(t) != 1 {
		t.Fatal("stale host confirmation changed DNS")
	}
	root, _ = decodeBindingMigrationConfig(h.config)
	view, _ = dnsHostsView(root)
	cb.Data = dnsHostCallback("edit", 1, view.ETag)
	s.handleDNSHostCallback(cb)
	if !s.handleDNSInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "alias.example"}) {
		t.Fatal("host update was not handled")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	view, _ = dnsHostsView(root)
	if view.Hosts[0].Targets[0] != "alias.example" {
		t.Fatalf("host targets not updated: %+v", view)
	}
	cb.Data = dnsHostCallback("rename", 1, view.ETag)
	s.handleDNSHostCallback(cb)
	if !s.handleDNSInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "full:example.com"}) {
		t.Fatal("host rename was not handled")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	view, _ = dnsHostsView(root)
	if view.Hosts[0].Name != "full:example.com" || view.Hosts[0].Targets[0] != "alias.example" {
		t.Fatalf("host not renamed: %+v", view)
	}
	cb.Data = dnsHostCallback("delete", 1, view.ETag)
	s.handleDNSHostCallback(cb)
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	view, _ = dnsHostsView(root)
	if len(view.Hosts) != 0 || h.restartCount(t) != 4 {
		t.Fatalf("host not deleted: %+v", view)
	}
}
