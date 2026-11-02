package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Defaults and yes/no answers can be chosen with buttons; they pass the same
// validation as typed answers and keep the wizard in one message.
func TestNodeWizardCanBeCompletedWithButtons(t *testing.T) {
	s, capture, chat, admin := inputSessionBot(t)
	t.Cleanup(func() { clearInputSessions(chat.ID) })
	// The summary generates REALITY keys with the core's x25519 helper.
	dir := t.TempDir()
	fake := "#!/bin/sh\nprintf 'PrivateKey: cHJpdmF0ZS1rZXktZm9yLXRlc3RpbmctMDEyMzQ1Njc4OQ\nPassword (PublicKey): cHVibGljLWtleS1mb3ItdGVzdGluZy0wMTIzNDU2Nzg5\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "xray"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	press := func(value string) {
		t.Helper()
		s.handleCallback(&tgCallback{ID: "q", From: admin, Message: &tgMessage{MessageID: 21, Chat: chat}, Data: "node_new_step:" + value})
	}
	step := func() string {
		nodeCreateStates.Lock()
		defer nodeCreateStates.Unlock()
		return nodeCreateStates.items[chat.ID].Step
	}

	s.beginNodeCreate(chat.ID, admin.ID, 21, NodeTemplateTCPReality)
	if _, keyboard := capture.card(t); !strings.Contains(strings.Join(keyboardCallbacks(t, keyboard), " "), "node_new_step:-") {
		t.Fatal("port step offers no default button")
	}
	press("rm -rf")
	if step() != "port" {
		t.Fatal("an unknown button value changed the wizard")
	}
	for _, answer := range []string{"-", "-", "-"} {
		press(answer)
	}
	if step() != "encryption" {
		t.Fatalf("defaults did not reach the encryption step: %q", step())
	}
	if _, keyboard := capture.card(t); !strings.Contains(strings.Join(keyboardCallbacks(t, keyboard), " "), "node_new_step:y") {
		t.Fatal("encryption step offers no yes/no buttons")
	}
	press("n")
	press("n")
	nodeCreateStates.Lock()
	_, active := nodeCreateStates.items[chat.ID]
	nodeCreateStates.Unlock()
	text, method := capturedRequest(capture)
	if active || !strings.HasSuffix(method, "/editMessageText") || !strings.Contains(plainHTML(htmlText(text)), "443") {
		t.Fatalf("wizard did not end on its summary in place: active=%v via %s: %q", active, method, text)
	}
}
