package main

import (
	"strings"
	"testing"
)

func capturedRequest(capture *capturedTelegram) (string, string) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.text, capture.method
}

// A slow screen must not replace the screen an admin opened while it loaded.
func TestSlowScreenDoesNotOverwriteANewerScreen(t *testing.T) {
	s, capture, chat, _ := inputSessionBot(t)
	release := make(chan struct{})
	s.renderLater(chat.ID, 10, "waiting", func() (htmlText, tgKeyboard) {
		<-release
		return "late result", tgKeyboard{}
	})
	if text, _ := capturedRequest(capture); text != "waiting" {
		t.Fatalf("waiting notice = %q", text)
	}
	s.sendOrEdit(chat.ID, 10, "newer screen", tgKeyboard{})
	close(release)
	backgroundWork.Wait()
	if text, _ := capturedRequest(capture); text != "newer screen" {
		t.Fatalf("late result replaced the newer screen: %q", text)
	}

	release = make(chan struct{})
	s.renderLater(chat.ID, 11, "waiting", func() (htmlText, tgKeyboard) {
		<-release
		return "fresh result", tgKeyboard{}
	})
	close(release)
	backgroundWork.Wait()
	if text, method := capturedRequest(capture); text != "fresh result" || !strings.HasSuffix(method, "/editMessageText") {
		t.Fatalf("result was not shown in place: %q via %s", text, method)
	}
}

// Outcomes such as update results always reach the admin.
func TestDeliveredResultArrivesAfterTheAdminMovesOn(t *testing.T) {
	s, capture, chat, _ := inputSessionBot(t)
	release := make(chan struct{})
	s.deliverLater(chat.ID, 12, "working", func() (htmlText, tgKeyboard) {
		<-release
		return "update finished", tgKeyboard{}
	})
	s.sendOrEdit(chat.ID, 12, "another screen", tgKeyboard{})
	close(release)
	backgroundWork.Wait()
	if text, method := capturedRequest(capture); text != "update finished" || !strings.HasSuffix(method, "/sendMessage") {
		t.Fatalf("result was not delivered as a new message: %q via %s", text, method)
	}
}

// Update buttons are easy to tap by accident, so they ask first.
func TestUpdateButtonsAskForConfirmation(t *testing.T) {
	s, capture, chat, admin := inputSessionBot(t)
	s.cfg.AllowUpdates = true
	s.handleCallback(&tgCallback{ID: "q", From: admin, Message: &tgMessage{MessageID: 13, Chat: chat}, Data: "update_all"})
	_, keyboard := capture.card(t)
	token := ""
	for _, callback := range keyboardCallbacks(t, keyboard) {
		if strings.HasPrefix(callback, "update_confirm:") {
			token = strings.TrimPrefix(callback, "update_confirm:")
		}
	}
	pendingUpdates.Lock()
	pending, ok := pendingUpdates.items[token]
	delete(pendingUpdates.items, token)
	pendingUpdates.Unlock()
	if token == "" || !ok || pending.Component != "all" || pending.AdminID != admin.ID {
		t.Fatalf("update_all did not ask for confirmation: token=%q pending=%+v", token, pending)
	}
	if !safeUpdateMutex.TryLock() {
		t.Fatal("an update started before confirmation")
	}
	safeUpdateMutex.Unlock()
}
