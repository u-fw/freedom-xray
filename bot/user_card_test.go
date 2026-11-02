package main

import (
	"strings"
	"testing"
	"time"
)

func TestUserCardPutsSharingFirstAndHidesChangesWhenReadOnly(t *testing.T) {
	h := newBindingMigrationHarness(t, bindingMigrationConfig(), false)
	cfg := &BotConfig{Language: "en", LanguageExplicit: true, AdminIDs: []int64{17}, XrayConfigPath: h.config, XrayBinPath: h.xray}
	s, capture := newCapturedRoutingBot(t, cfg)
	key := userCallbackKey("alice")

	s.sendUserManageCard(9820, 4, key)
	_, keyboard := capture.card(t)
	callbacks := keyboardCallbacks(t, keyboard)
	if len(callbacks) == 0 || callbacks[0] != "user_links:"+key {
		t.Fatalf("sharing is not the first action: %v", callbacks)
	}
	for _, callback := range callbacks {
		for _, change := range []string{"manage_node:", "manage_delete:", "manage_quota:", "manage_rotate:", "manage_disable:"} {
			if strings.HasPrefix(callback, change) {
				t.Fatalf("read-only card offers %s", callback)
			}
		}
	}

	cfg.AllowMutations = true
	s.sendUserManageCard(9820, 4, key)
	_, keyboard = capture.card(t)
	joined := strings.Join(keyboardCallbacks(t, keyboard), " ")
	for _, want := range []string{"manage_node:", "manage_quota:" + key, "manage_delete:" + key, "manage_traffic:" + key, "menu_users", "menu_main"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("card lacks %s: %s", want, joined)
		}
	}
	if strings.Contains(joined, "manage_limitclear:") {
		t.Fatal("clear-limits offered for a user without limits")
	}
}

// After a confirmed change the admin lands on the changed user's card, so the
// next step (sharing links, setting a quota) is one tap away.
func TestConfirmedUserChangeReturnsToTheUserCard(t *testing.T) {
	h := newBindingMigrationHarness(t, bindingMigrationConfig(), false)
	cfg := &BotConfig{Language: "en", LanguageExplicit: true, AllowMutations: true, AdminIDs: []int64{17}, XrayConfigPath: h.config, XrayBinPath: h.xray, StatsAPIServer: "127.0.0.1:10085"}
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgChat{ID: 9821, Type: "private"}, &tgUser{ID: 17}

	s.offerUserChange(chat.ID, admin.ID, 0, UserChange{Kind: "add", User: "phone", Node: "raw"})
	_, keyboard := capture.card(t)
	confirm := ""
	for _, callback := range keyboardCallbacks(t, keyboard) {
		if strings.HasPrefix(callback, "manage_confirm:") {
			confirm = callback
		}
	}
	if confirm == "" {
		t.Fatal("no confirmation offered")
	}
	s.resolveUserChange(&tgCallback{ID: "q", From: admin, Message: &tgMessage{MessageID: 6, Chat: chat}, Data: confirm}, true)
	text, keyboard := capture.card(t)
	callbacks := keyboardCallbacks(t, keyboard)
	if !strings.Contains(text, "User: phone") || len(callbacks) == 0 || callbacks[0] != "user_links:"+userCallbackKey("phone") {
		t.Fatalf("did not land on the new user's card: %q %v", text, callbacks)
	}
}

func TestUsageMeterAndExpiryCountdown(t *testing.T) {
	if got := usageMeter(0, 100); got != "<code>▱▱▱▱▱▱▱▱▱▱</code> 0%" {
		t.Fatalf("empty meter = %q", got)
	}
	if got := usageMeter(42, 100); got != "<code>▰▰▰▰▱▱▱▱▱▱</code> 42%" {
		t.Fatalf("partial meter = %q", got)
	}
	if got := usageMeter(150, 100); got != "<code>▰▰▰▰▰▰▰▰▰▰</code> 150%" {
		t.Fatalf("exceeded meter = %q", got)
	}
	if usageMeter(10, 0) != "" {
		t.Fatal("a meter without a quota")
	}
	now := time.Unix(1_800_000_000, 0)
	if got := expiryCountdown(userLimit{ExpiryUnix: now.Add(36 * time.Hour).Unix()}, now, "en"); got != " · 2 day(s) left" {
		t.Fatalf("future expiry = %q", got)
	}
	if got := expiryCountdown(userLimit{ExpiryUnix: now.Add(-time.Hour).Unix()}, now, "en"); got != " · expired" {
		t.Fatalf("past expiry = %q", got)
	}
	if expiryCountdown(userLimit{}, now, "en") != "" {
		t.Fatal("countdown without an expiry")
	}
}
