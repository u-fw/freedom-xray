package main

import (
	"strings"
	"testing"
)

// The help card is generated from the commands registered with Telegram, so
// the two cannot drift apart.
func TestHelpCardListsEveryRegisteredCommand(t *testing.T) {
	s, capture, chat, _ := inputSessionBot(t)
	s.sendHelpCard(chat.ID, 0)
	text, _ := capturedRequest(capture)
	for _, command := range adminCommands("en") {
		if !strings.Contains(text, "/"+command.Command) || !strings.Contains(text, string(esc(command.Description))) {
			t.Fatalf("help omits /%s: %q", command.Command, text)
		}
	}
	if !strings.Contains(text, "<code>/adduser &lt;user&gt; [inbound|all]</code>") {
		t.Fatalf("argument syntax missing or unescaped: %q", text)
	}
}

// Backups contain credentials; the Tools card offers them only when allowed.
func TestToolsCardOffersBackupOnlyWhenAllowed(t *testing.T) {
	s, capture, chat, _ := inputSessionBot(t)
	s.sendToolsCard(chat.ID, 0)
	_, keyboard := capture.card(t)
	if strings.Contains(strings.Join(keyboardCallbacks(t, keyboard), " "), "menu_backup") {
		t.Fatal("backup offered while sensitive exports are disabled")
	}
	s.cfg.AllowSensitiveBackup = true
	s.sendToolsCard(chat.ID, 0)
	_, keyboard = capture.card(t)
	if !strings.Contains(strings.Join(keyboardCallbacks(t, keyboard), " "), "menu_backup") {
		t.Fatal("backup missing while sensitive exports are allowed")
	}
}
