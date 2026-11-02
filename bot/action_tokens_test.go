package main

import (
	"strings"
	"testing"
	"time"
)

func TestServiceConfirmationsAreOneTimeAndOwned(t *testing.T) {
	s, capture, chat, admin := inputSessionBot(t)
	press := func(data string, from *tgUser) bool {
		return s.takeConfirmation(&tgCallback{ID: "q", From: from, Message: &tgMessage{MessageID: 30, Chat: chat}, Data: data}, "menu_restart_confirm", "restart")
	}
	data := confirmData("menu_restart_confirm", "restart", chat.ID, admin.ID)
	if !strings.HasPrefix(data, "menu_restart_confirm:") || len(data) > 64 {
		t.Fatalf("unexpected confirm data %q", data)
	}
	if press(data, &tgUser{ID: 99}) {
		t.Fatal("another admin used the confirmation")
	}
	if press("menu_restart_confirm", admin) {
		t.Fatal("a legacy button without a token was accepted")
	}
	other := confirmData("menu_restart_confirm", "traffic_reset", chat.ID, admin.ID)
	if press(other, admin) {
		t.Fatal("a token for another action was accepted")
	}
	if !press(data, admin) {
		t.Fatal("the owner could not confirm")
	}
	if press(data, admin) {
		t.Fatal("a confirmation was used twice")
	}
	stale := confirmData("menu_restart_confirm", "restart", chat.ID, admin.ID)
	actionConfirmations.Lock()
	token := strings.TrimPrefix(stale, "menu_restart_confirm:")
	item := actionConfirmations.items[token]
	item.Expires = time.Now().Add(-time.Second)
	actionConfirmations.items[token] = item
	actionConfirmations.Unlock()
	if press(stale, admin) {
		t.Fatal("an expired confirmation was accepted")
	}

	// Pressing an old button through the dispatcher explains what happened.
	s.handleCallback(&tgCallback{ID: "q", From: admin, Message: &tgMessage{MessageID: 31, Chat: chat}, Data: "traffic_reset_confirm"})
	if text, _ := capturedRequest(capture); !strings.Contains(text, "expired or was already used") {
		t.Fatalf("legacy confirm button = %q", text)
	}
}
