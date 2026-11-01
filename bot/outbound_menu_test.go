package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestOutboundBotDetailsAndEndpointEdit(t *testing.T) {
	data, _ := json.Marshal(outboundFixture(t))
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9511, Type: "private"}, &tgbotapi.User{ID: 17}
	root, _ := decodeBindingMigrationConfig(h.config)
	records, _ := outboundRecords(root)
	record := records[3]
	s.sendOutboundDetail(chat.ID, 1, record.Index, record.ETag)
	body, markup := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(body, "old.example") || strings.Contains(body, "secret-id") || !strings.Contains(callbacks, "ob:address:") {
		t.Fatalf("bad outbound card: %s %s", body, callbacks)
	}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: outboundCallback("address", record.Index, record.ETag)}
	s.handleOutboundCallback(cb)
	before, _ := os.ReadFile(h.config)
	if !s.handleOutboundInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "new.example"}) {
		t.Fatal("address input not handled")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("outbound changed before confirmation")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	values, _ := outboundValues(root)
	updated, _ := json.Marshal(values[3])
	if !bytes.Contains(updated, []byte("new.example")) || !bytes.Contains(updated, []byte("secret-id")) || h.restartCount(t) != 1 {
		t.Fatalf("incorrect outbound edit: %s", updated)
	}
	s.resolveRoutingChange(cb, true)
	if h.restartCount(t) != 1 {
		t.Fatal("confirmation replayed")
	}
}

func TestOutboundBotCreatesAndProtectsDefault(t *testing.T) {
	h := newBindingMigrationHarness(t, editableRoutingFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9512, Type: "private"}, &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "ob:new:blackhole"}
	s.handleOutboundCallback(cb)
	if !s.handleOutboundInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "new-block"}) {
		t.Fatal("tag input not handled")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ := decodeBindingMigrationConfig(h.config)
	records, _ := outboundRecords(root)
	if records[len(records)-1].Tag != "new-block" || h.restartCount(t) != 1 {
		t.Fatal("outbound not created")
	}
	s.sendOutboundDetail(chat.ID, 1, 1, records[0].ETag)
	_, markup := capture.card(t)
	for _, callback := range keyboardCallbacks(t, markup) {
		if strings.HasPrefix(callback, "ob:delete:") {
			t.Fatal("default outbound exposes delete")
		}
	}
}
