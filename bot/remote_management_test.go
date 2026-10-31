package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func testRemoteBot(t *testing.T, cfg *BotConfig) *BotService {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","message_id":1}}`)
	}))
	t.Cleanup(server.Close)
	bot, err := tgbotapi.NewBotAPIWithClient("test", server.URL+"/bot%s/%s", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &BotService{cfg: cfg, bot: bot}
}

func TestRoutingWizardRequiresBoundConfirmation(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	cfg.XrayBinPath = h.xray
	cfg.AllowMutations = true
	s := testRemoteBot(t, cfg)
	chat := &tgbotapi.Chat{ID: 9421, Type: "private"}
	admin := &tgbotapi.User{ID: 17}
	other := &tgbotapi.User{ID: 18}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "routing_manage:add"}
	s.beginRoutingWizard(cb)
	msg := &tgbotapi.Message{From: admin, Chat: chat, Text: `{"ruleTag":"ads","domain":["domain:example.com"],"outboundTag":"block"}`}
	before, _ := os.ReadFile(h.config)
	if !s.handleRoutingInput(msg) {
		t.Fatal("routing input was not captured")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("routing input changed config before confirmation")
	}
	pendingRoutingChanges.Lock()
	var key string
	for token, entry := range pendingRoutingChanges.items {
		if entry.ChatID == chat.ID {
			key = token
		}
	}
	pendingRoutingChanges.Unlock()
	if key == "" {
		t.Fatal("missing pending confirmation")
	}
	cb.Data = "routing_confirm:" + key
	cb.From = other
	s.resolveRoutingChange(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(before, after) {
		t.Fatal("another admin confirmed the action")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	after, _ = os.ReadFile(h.config)
	if bytes.Equal(before, after) || !strings.Contains(string(after), `"ruleTag": "ads"`) || h.restartCount(t) != 1 {
		t.Fatal("authorized confirmation did not apply the rule")
	}
	s.resolveRoutingChange(cb, true)
	if h.restartCount(t) != 1 {
		t.Fatal("confirmation was replayed")
	}
}

func TestServiceControlsRespectPermissionAndOneTimeConfirmation(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	statePath := h.systemctl + ".state"
	script := "#!/bin/sh\ncase \"$1\" in\n start) printf active > '" + statePath + "' ;;\n stop) printf inactive > '" + statePath + "' ;;\n show) cat '" + statePath + "' ;;\nesac\n"
	if err := os.WriteFile(h.systemctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	cfg.XrayBinPath = h.xray
	s := testRemoteBot(t, cfg)
	chat := &tgbotapi.Chat{ID: 9431, Type: "private"}
	admin := &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "service_ask:stop"}
	s.askServiceAction(cb, "stop")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("disabled remote service control ran")
	}
	cfg.AllowMutations = true
	s.askServiceAction(cb, "stop")
	pendingServiceActions.Lock()
	var key string
	for token, entry := range pendingServiceActions.items {
		if entry.chatID == chat.ID && time.Now().Before(entry.expires) {
			key = token
		}
	}
	pendingServiceActions.Unlock()
	if key == "" {
		t.Fatal("missing confirmation")
	}
	cb.Data = "service_confirm:" + key
	cfg.AllowMutations = false
	s.resolveServiceAction(cb)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("stale confirmation bypassed the permission")
	}
	cfg.AllowMutations = true
	if err := manageXrayService(cfg, "start"); err != nil {
		t.Fatal(err)
	}
	if err := manageXrayService(cfg, "stop"); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(statePath)
	if err != nil || string(state) != "inactive" {
		t.Fatalf("service not stopped: %q %v", state, err)
	}
}
