package main

import (
	"strings"
	"testing"
)

func inputSessionBot(t *testing.T) (*BotService, *capturedTelegram, *tgChat, *tgUser) {
	t.Helper()
	cfg := &BotConfig{Language: "en", LanguageExplicit: true, AllowMutations: true, AdminIDs: []int64{17}, XrayConfigPath: writeRoutingConfig(t, `{"inbounds":[],"outbounds":[{"tag":"direct","protocol":"freedom"}]}`)}
	s, capture := newCapturedRoutingBot(t, cfg)
	return s, capture, &tgChat{ID: 9750, Type: "private"}, &tgUser{ID: 17}
}

func commandMessage(chat *tgChat, from *tgUser, text string) *tgMessage {
	length := len(text)
	if space := strings.IndexByte(text, ' '); space > 0 {
		length = space
	}
	return &tgMessage{Chat: chat, From: from, Text: text, Entities: []tgEntity{{Type: "bot_command", Offset: 0, Length: length}}}
}

func lastCapturedText(capture *capturedTelegram) string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.text
}

// Only one input flow may own a chat's next message.
func TestStartingAnInputFlowEndsOtherFlows(t *testing.T) {
	s, _, chat, admin := inputSessionBot(t)
	t.Cleanup(func() { clearInputSessions(chat.ID) })
	s.startNodeWizard(chat.ID, admin.ID, 0, "raw", "public_port", "port?")
	s.beginUserWizard(chat.ID, admin.ID, 0, "name", "")
	nodePathWizards.Lock()
	_, nodeActive := nodePathWizards.items[chat.ID]
	nodePathWizards.Unlock()
	managementWizards.Lock()
	_, nameActive := managementWizards.states[chat.ID]
	managementWizards.Unlock()
	if nodeActive || !nameActive {
		t.Fatalf("expected only the user name flow, node=%v name=%v", nodeActive, nameActive)
	}
	s.beginUserLimitWizard(chat.ID, admin.ID, 0, "alice", "quota", "quota?")
	managementWizards.Lock()
	_, nameActive = managementWizards.states[chat.ID]
	managementWizards.Unlock()
	if nameActive {
		t.Fatal("starting the quota flow left the user name flow active")
	}
}

// A typo must not discard the flow: the admin corrects the value instead of
// navigating back through the menus.
func TestInvalidInputKeepsTheFlowOpen(t *testing.T) {
	s, capture, chat, admin := inputSessionBot(t)
	t.Cleanup(func() { clearInputSessions(chat.ID) })

	s.startNodeWizard(chat.ID, admin.ID, 0, "raw", "public_port", "port?")
	if !s.handleNodePathInput(&tgMessage{Chat: chat, From: admin, Text: "not-a-port"}) {
		t.Fatal("node input was not handled")
	}
	nodePathWizards.Lock()
	_, active := nodePathWizards.items[chat.ID]
	nodePathWizards.Unlock()
	if !active || !strings.Contains(lastCapturedText(capture), "Send a corrected value") {
		t.Fatalf("invalid port closed the flow or omitted the retry hint: %q", lastCapturedText(capture))
	}
	if !s.handleNodePathInput(commandMessage(chat, admin, "/cancel@freedom_bot")) {
		t.Fatal("/cancel with a bot suffix was not handled")
	}
	nodePathWizards.Lock()
	_, active = nodePathWizards.items[chat.ID]
	nodePathWizards.Unlock()
	if active {
		t.Fatal("/cancel did not close the node flow")
	}

	s.beginUserLimitWizard(chat.ID, admin.ID, 0, "alice", "quota", "quota?")
	s.handleUserLimitInput(&tgMessage{Chat: chat, From: admin, Text: "lots"})
	userLimitWizards.Lock()
	_, active = userLimitWizards.items[chat.ID]
	userLimitWizards.Unlock()
	if !active {
		t.Fatal("invalid quota closed the flow")
	}

	s.beginUserWizard(chat.ID, admin.ID, 0, "name", "")
	s.handleWizardMessage(&tgMessage{Chat: chat, From: admin, Text: "bad name!"})
	managementWizards.Lock()
	_, active = managementWizards.states[chat.ID]
	managementWizards.Unlock()
	if !active || !strings.Contains(lastCapturedText(capture), "Invalid name") {
		t.Fatalf("invalid name closed the flow: %q", lastCapturedText(capture))
	}
	s.handleWizardMessage(&tgMessage{Chat: chat, From: admin, Text: "good_name"})
	managementWizards.Lock()
	state := managementWizards.states[chat.ID]
	managementWizards.Unlock()
	if state.Stage != "scope" || state.User != "good_name" {
		t.Fatalf("a valid name did not advance to inbound selection: %+v", state)
	}
}

func TestCancelAndStrayTextExplainWhatHappened(t *testing.T) {
	s, capture, chat, admin := inputSessionBot(t)
	s.handleMessage(commandMessage(chat, admin, "/cancel"))
	if text := lastCapturedText(capture); !strings.Contains(text, "Nothing to cancel") {
		t.Fatalf("/cancel without input = %q", text)
	}
	s.handleMessage(&tgMessage{Chat: chat, From: admin, Text: "hello"})
	if text := lastCapturedText(capture); !strings.Contains(text, "No input is pending") {
		t.Fatalf("stray text = %q", text)
	}
}

func TestStaleButtonOpensTheCurrentDashboard(t *testing.T) {
	s, capture, chat, admin := inputSessionBot(t)
	s.handleCallback(&tgCallback{ID: "q", From: admin, Message: &tgMessage{MessageID: 4, Chat: chat}, Data: "obsolete_menu:1"})
	capture.mu.Lock()
	text, method := capture.text, capture.method
	capture.mu.Unlock()
	if !strings.Contains(text, "older menu") || !strings.HasSuffix(method, "/editMessageText") {
		t.Fatalf("stale button = %q via %s", text, method)
	}
}
