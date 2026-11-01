package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestDNSBotEditPreservesSettingsAndHidesSecrets(t *testing.T) {
	root := agentFixtureRoot(t)
	root["dns"] = map[string]any{"servers": []any{map[string]any{"address": "https://user:password@dns.example/secret?token=one", "domains": []any{"domain:example.com"}}}}
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9521, Type: "private"}, &tgbotapi.User{ID: 17}
	view, _ := dnsView(root)
	s.sendDNSPage(chat.ID, 1, 0, "")
	body, markup := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, markup), " ")
	encodedMarkup, _ := json.Marshal(markup)
	visible := body + string(encodedMarkup)
	if strings.Contains(visible, "password") || strings.Contains(visible, "token=one") || !strings.Contains(visible, "dns.example") || !strings.Contains(callbacks, "dn:v:") {
		t.Fatalf("unsafe DNS page: %s %s", body, callbacks)
	}
	s.sendDNSServerDetail(chat.ID, 1, 1, view.ETag)
	body, markup = capture.card(t)
	if strings.Contains(body, "password") || strings.Contains(body, "token=one") || !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "dn:up:") {
		t.Fatal("unsafe or incomplete DNS detail")
	}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: dnsCallback("edit", 1, view.ETag)}
	s.handleDNSCallback(cb)
	before, _ := os.ReadFile(h.config)
	if !s.handleDNSInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "https://other.example/dns-query?token=two"}) {
		t.Fatal("DNS input not handled")
	}
	body, _ = capture.card(t)
	if strings.Contains(body, "token=two") || strings.Contains(body, "password") {
		t.Fatal("DNS confirmation exposed credentials")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("DNS changed before confirmation")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgbotapi.User{ID: 18}
	s.resolveRoutingChange(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(before, after) {
		t.Fatal("another admin confirmed DNS change")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	config, _ := readDNS(root)
	servers, _ := dnsServers(config)
	server := servers[0].(map[string]any)
	if server["address"] != "https://other.example/dns-query?token=two" || server["domains"].([]any)[0] != "domain:example.com" || h.restartCount(t) != 1 {
		t.Fatalf("DNS object was not preserved: %#v", server)
	}
}

func TestDNSBotCreateSwitchAndStaleConfirmation(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9522, Type: "private"}, &tgbotapi.User{ID: 17}
	root, _ := decodeBindingMigrationConfig(h.config)
	view, _ := dnsView(root)
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "dn:add:" + view.ETag}
	s.handleDNSCallback(cb)
	if !s.handleDNSInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "1.1.1.1"}) {
		t.Fatal("DNS server input not handled")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	view, _ = dnsView(root)
	if len(view.Servers) != 1 || h.restartCount(t) != 1 {
		t.Fatalf("DNS not created: %+v", view)
	}
	s.sendDNSPage(chat.ID, 1, 0, "")
	_, markup := capture.card(t)
	keyboardCallbacks(t, markup)
	cb.Data = "dn:f:cache:" + view.ETag
	s.handleDNSCallback(cb)
	token := pendingRoutingToken(t, chat.ID)
	root["dns"].(map[string]any)["queryStrategy"] = "UseIPv4"
	data, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	cb.Data = "routing_confirm:" + token
	s.resolveRoutingChange(cb, true)
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(data, after) || h.restartCount(t) != 1 {
		t.Fatal("stale DNS switch changed configuration")
	}
}
