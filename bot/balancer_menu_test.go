package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestBalancerBotCreateEditAndStaleConfirmation(t *testing.T) {
	h := newBindingMigrationHarness(t, editableRoutingFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9501, Type: "private"}, &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "bl:add"}
	s.handleBalancerCallback(cb)
	if !s.handleBalancerInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "pool"}) {
		t.Fatal("tag was not handled")
	}
	before, _ := os.ReadFile(h.config)
	if !s.handleBalancerInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "direct\nblock"}) {
		t.Fatal("selectors were not handled")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("creation changed configuration before confirmation")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgbotapi.User{ID: 18}
	s.resolveRoutingChange(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(before, after) {
		t.Fatal("another administrator confirmed creation")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	root, err := decodeBindingMigrationConfig(h.config)
	if err != nil {
		t.Fatal(err)
	}
	records, err := balancerRecords(root)
	if err != nil || len(records) != 1 || records[0].Tag != "pool" || h.restartCount(t) != 1 {
		t.Fatalf("balancer not created: %#v %v", records, err)
	}

	s.sendBalancerDetail(chat.ID, 1, userCallbackKey("pool"), records[0].ETag)
	body, markup := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(body, "direct") || !strings.Contains(body, "random") || !strings.Contains(callbacks, "bl:strategy:") {
		t.Fatalf("incomplete detail: %s %s", body, callbacks)
	}
	cb.Data = balancerCallback("strategy", "pool", records[0].ETag)
	s.handleBalancerCallback(cb)
	_, markup = capture.card(t)
	keyboardCallbacks(t, markup)
	cb.Data = balancerCallback("fallback", "pool", records[0].ETag)
	s.handleBalancerCallback(cb)
	_, markup = capture.card(t)
	callbacks = strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(callbacks, "bl:sf:") {
		t.Fatalf("fallback picker missing: %s", callbacks)
	}

	cb.Data = balancerCallback("selector", "pool", records[0].ETag)
	s.handleBalancerCallback(cb)
	if !s.handleBalancerInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "block"}) {
		t.Fatal("edit selector not handled")
	}
	token := pendingRoutingToken(t, chat.ID)
	root, _ = decodeBindingMigrationConfig(h.config)
	_, existing, _ := findBalancer(root, "pool")
	existing["selector"] = []any{"direct"}
	data, _ := json.Marshal(root)
	if err = os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	cb.Data = "routing_confirm:" + token
	s.resolveRoutingChange(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(data, after) || h.restartCount(t) != 1 {
		t.Fatal("stale selector confirmation changed config")
	}
}

func TestBalancerWarnsAboutMissingCoverageAndFallback(t *testing.T) {
	root := editableRoutingRoot(t)
	root["observatory"] = map[string]any{"subjectSelector": []any{"block"}}
	item := map[string]any{"tag": "pool", "selector": []any{"direct"}, "strategy": map[string]any{"type": "leastPing"}}
	warning := balancerWarnings("en", root, item)
	if !strings.Contains(warning, "direct") || !strings.Contains(warning, "fallback") {
		t.Fatalf("missing monitor warning: %s", warning)
	}
	root["observatory"] = map[string]any{"subjectSelector": []any{"direct"}}
	item["fallbackTag"] = "block"
	if warning = balancerWarnings("en", root, item); warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
}
