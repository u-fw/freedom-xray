package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddUserToSelectedInboundsInOneChange(t *testing.T) {
	h := newBindingMigrationHarness(t, bindingMigrationConfig(), false)
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "add", User: "tablet", Nodes: []string{"missing"}}); err == nil {
		t.Fatal("an unknown inbound was accepted")
	}
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "add", User: "tablet", Nodes: []string{"raw", "cdn"}}); err != nil {
		t.Fatal(err)
	}
	emails := h.configEmails(t)
	assigned := 0
	for _, email := range emails {
		if bindingOwner(email) == "tablet" {
			assigned++
		}
	}
	if assigned != 2 {
		t.Fatalf("expected tablet in both inbounds, got %d bindings: %v", assigned, emails)
	}
}

// New users can be assigned to several inbounds with toggles and a single
// confirmation instead of one confirmation and restart per inbound.
func TestAddUserWizardSelectsSeveralInbounds(t *testing.T) {
	h := newBindingMigrationHarness(t, bindingMigrationConfig(), false)
	cfg := &BotConfig{Language: "en", LanguageExplicit: true, AllowMutations: true, AdminIDs: []int64{17}, XrayConfigPath: h.config, XrayBinPath: h.xray, StatsAPIServer: "127.0.0.1:10085"}
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgChat{ID: 9840, Type: "private"}, &tgUser{ID: 17}
	t.Cleanup(func() { clearInputSessions(chat.ID) })
	press := func(data string) {
		t.Helper()
		s.handleCallback(&tgCallback{ID: "q", From: admin, Message: &tgMessage{MessageID: 41, Chat: chat}, Data: data})
	}

	s.beginUserWizard(chat.ID, admin.ID, 0, "name", "")
	s.handleWizardMessage(&tgMessage{Chat: chat, From: admin, Text: "tablet"})
	press("manage_pick:" + userCallbackKey("raw"))
	press("manage_pick:" + userCallbackKey("cdn"))
	press("manage_pick:" + userCallbackKey("raw"))
	press("manage_pick:" + userCallbackKey("raw"))
	text, keyboard := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, keyboard), " ")
	if !strings.Contains(callbacks, "manage_scope:picked") || !strings.Contains(plainHTML(htmlText(text)), "tablet") {
		t.Fatalf("selection screen = %q %s", text, callbacks)
	}
	labels := ""
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			labels += button.Text + "|"
		}
	}
	if !strings.Contains(labels, "☑ raw") || !strings.Contains(labels, "☑ cdn") || !strings.Contains(labels, "(2)") {
		t.Fatalf("selection state not shown: %s", labels)
	}

	press("manage_scope:picked")
	text, keyboard = capture.card(t)
	if !strings.Contains(text, "cdn, raw") && !strings.Contains(text, "raw, cdn") {
		t.Fatalf("confirmation does not name both inbounds: %q", text)
	}
	confirm := ""
	for _, callback := range keyboardCallbacks(t, keyboard) {
		if strings.HasPrefix(callback, "manage_confirm:") {
			confirm = callback
		}
	}
	press(confirm)
	text, keyboard = capture.card(t)
	if !strings.Contains(text, "User: tablet") || keyboardCallbacks(t, keyboard)[0] != "user_links:"+userCallbackKey("tablet") {
		t.Fatalf("did not land on the new user's card: %q", text)
	}
	assigned := 0
	for _, email := range h.configEmails(t) {
		if bindingOwner(email) == "tablet" {
			assigned++
		}
	}
	if assigned != 2 {
		t.Fatalf("expected two bindings for tablet, got %d", assigned)
	}
}

func TestAgentAddsUserToSeveralInbounds(t *testing.T) {
	h := newBindingMigrationHarness(t, bindingMigrationConfig(), false)
	botConfig := filepath.Join(t.TempDir(), "bot.json")
	settings, _ := json.Marshal(map[string]any{"xray_config_path": h.config, "xray_bin_path": h.xray, "stats_api_server": "127.0.0.1:10085"})
	if err := os.WriteFile(botConfig, settings, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(input string, args ...string) agentResponse {
		t.Helper()
		var out bytes.Buffer
		runAgentCLI(append(args, "--config", botConfig), strings.NewReader(input), &out)
		var response agentResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("bad response %q: %v", out.String(), err)
		}
		return response
	}
	if run(`{"user":"alice","nodes":["raw"]}`, "users.rotate", "--apply").OK {
		t.Fatal("nodes accepted for users.rotate")
	}
	if run(`{"user":"tablet","node":"all","nodes":["raw"]}`, "users.add", "--apply").OK {
		t.Fatal("nodes accepted together with node all")
	}
	if response := run(`{"user":"tablet","nodes":["raw","cdn"]}`, "users.add", "--apply"); !response.OK {
		t.Fatalf("users.add with nodes failed: %+v", response.Error)
	}
	assigned := 0
	for _, email := range h.configEmails(t) {
		if bindingOwner(email) == "tablet" {
			assigned++
		}
	}
	if assigned != 2 {
		t.Fatalf("expected two bindings for tablet, got %d", assigned)
	}
}
