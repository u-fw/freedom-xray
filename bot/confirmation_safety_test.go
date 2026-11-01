package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestNodeDeleteConfirmationIsBoundFreshAndSingleUse(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat := &tgbotapi.Chat{ID: 9541, Type: "private"}
	admin := &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}}
	show := func() string {
		t.Helper()
		s.showNodeDeleteConfirm(chat.ID, admin.ID, 1, userCallbackKey("raw"))
		_, markup := capture.card(t)
		for _, callback := range keyboardCallbacks(t, markup) {
			if strings.HasPrefix(callback, "node_delete_confirm:") {
				if callback == "node_delete_confirm:"+userCallbackKey("raw") {
					t.Fatal("delete confirmation uses a predictable reusable callback")
				}
				return callback
			}
		}
		t.Fatal("missing delete confirmation")
		return ""
	}
	first := show()
	cb.Data, cb.From = first, &tgbotapi.User{ID: 18}
	before, _ := os.ReadFile(h.config)
	s.confirmNodeDelete(cb, true)
	if !strings.HasSuffix(capture.lastMethod(), "/sendMessage") {
		t.Fatal("another admin replaced the original confirmation card")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("another admin deleted the inbound")
	}
	root, _ := decodeBindingMigrationConfig(h.config)
	root["inbounds"].([]any)[0].(map[string]any)["port"] = json.Number("28444")
	changed, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, changed, 0600); err != nil {
		t.Fatal(err)
	}
	cb.From = admin
	s.confirmNodeDelete(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(changed, after) || h.restartCount(t) != 0 {
		t.Fatal("stale confirmation deleted a changed inbound")
	}
	cancel := show()
	cb.Data = strings.Replace(cancel, "node_delete_confirm:", "node_delete_cancel:", 1)
	s.confirmNodeDelete(cb, false)
	cb.Data = cancel
	s.confirmNodeDelete(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(changed, after) || h.restartCount(t) != 0 {
		t.Fatal("cancelled confirmation could still be used")
	}
	valid := show()
	cb.Data = valid
	s.confirmNodeDelete(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	if len(root["inbounds"].([]any)) != 0 || h.restartCount(t) != 1 {
		t.Fatal("fresh confirmation did not delete inbound")
	}
	if err := os.WriteFile(h.config, []byte(agentFixture), 0600); err != nil {
		t.Fatal(err)
	}
	s.confirmNodeDelete(cb, true)
	after, _ = os.ReadFile(h.config)
	if string(after) != agentFixture || h.restartCount(t) != 1 {
		t.Fatal("replayed confirmation deleted a new inbound with the same tag")
	}
}

func TestNodeDeleteRejectsChangedImpact(t *testing.T) {
	for _, change := range []string{"routing", "public-port"} {
		t.Run(change, func(t *testing.T) {
			h := newBindingMigrationHarness(t, agentFixture, false)
			_, etag, err := inspectNodeDeletionForConfirmation(h.config, "raw")
			if err != nil {
				t.Fatal(err)
			}
			if change == "routing" {
				root, _ := decodeBindingMigrationConfig(h.config)
				root["routing"].(map[string]any)["rules"] = []any{map[string]any{"type": "field", "inboundTag": []any{"raw"}, "outboundTag": "direct"}}
				data, _ := json.Marshal(root)
				if err := os.WriteFile(h.config, data, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(publicPortsPath(h.config), []byte(`{"nodes":{"raw":{"listen_port":28443,"public_port":443}}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(h.config)
			if _, err := deleteNodeWithExpectedImpact(h.config, h.xray, "127.0.0.1:10085", "raw", etag); !errors.Is(err, errNodeDeletionChanged) {
				t.Fatalf("changed impact was accepted: %v", err)
			}
			after, _ := os.ReadFile(h.config)
			if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
				t.Fatal("changed impact modified the configuration")
			}
		})
	}
}

func TestAgentNodeDeleteCanBindReviewedImpact(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	op, _ := findAgentOperation("nodes.delete")
	preview, err := executeAgentOperation(cfg, op, []byte(`{"tag":"raw"}`), false, false)
	if err != nil {
		t.Fatal(err)
	}
	report, ok := preview.(map[string]any)["preview"].(nodeDeletionReport)
	if !ok || len(report.ETag) != 24 {
		t.Fatalf("missing deletion edit reference: %#v", preview)
	}
	input, _ := json.Marshal(map[string]any{"tag": "raw", "etag": report.ETag})
	root, _ := decodeBindingMigrationConfig(h.config)
	root["inbounds"].([]any)[0].(map[string]any)["port"] = json.Number("28444")
	changed, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAgentOperation(cfg, op, input, true, false); !errors.Is(err, errNodeDeletionChanged) {
		t.Fatalf("agent accepted stale impact: %v", err)
	}
	if h.restartCount(t) != 0 {
		t.Fatal("stale agent request restarted Xray")
	}
	preview, err = executeAgentOperation(cfg, op, []byte(`{"tag":"raw"}`), false, false)
	if err != nil {
		t.Fatal(err)
	}
	report = preview.(map[string]any)["preview"].(nodeDeletionReport)
	input, _ = json.Marshal(map[string]any{"tag": "raw", "etag": report.ETag})
	if _, err := executeAgentOperation(cfg, op, input, true, false); err != nil {
		t.Fatal(err)
	}
	if h.restartCount(t) != 1 {
		t.Fatal("fresh agent deletion did not complete")
	}
}

func TestConfirmationOwnerCannotBeDisplaced(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AllowMutations = false
	s, capture := newCapturedRoutingBot(t, cfg)
	chat := &tgbotapi.Chat{ID: 9542, Type: "private"}
	cb := &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 18}, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}}
	expires := time.Now().Add(time.Minute)
	tests := []struct {
		name    string
		prepare func()
		invoke  func()
		exists  func() bool
	}{
		{"node", func() {
			pendingNodes.Lock()
			defer pendingNodes.Unlock()
			pendingNodes.items["test"] = pendingNodeChange{ChatID: chat.ID, AdminID: 17, Expires: expires}
		}, func() { cb.Data = "node_cancel:test"; s.confirmNodeChange(cb, false) }, func() bool {
			pendingNodes.Lock()
			defer pendingNodes.Unlock()
			_, ok := pendingNodes.items["test"]
			return ok
		}},
		{"node create", func() {
			pendingNodeCreates.Lock()
			defer pendingNodeCreates.Unlock()
			pendingNodeCreates.items["test"] = pendingNodeCreate{ChatID: chat.ID, AdminID: 17, Expires: expires}
		}, func() { cb.Data = "node_create_cancel:test"; s.confirmNodeCreate(cb, false) }, func() bool {
			pendingNodeCreates.Lock()
			defer pendingNodeCreates.Unlock()
			_, ok := pendingNodeCreates.items["test"]
			return ok
		}},
		{"user", func() {
			pendingUserChanges.Lock()
			defer pendingUserChanges.Unlock()
			pendingUserChanges.items["test"] = pendingUserChange{ChatID: chat.ID, AdminID: 17, ExpiresAt: expires}
		}, func() { cb.Data = "manage_cancel:test"; s.resolveUserChange(cb, false) }, func() bool {
			pendingUserChanges.Lock()
			defer pendingUserChanges.Unlock()
			_, ok := pendingUserChanges.items["test"]
			return ok
		}},
		{"update", func() {
			pendingUpdates.Lock()
			defer pendingUpdates.Unlock()
			pendingUpdates.items["test"] = pendingUpdate{ChatID: chat.ID, AdminID: 17, Deadline: expires}
		}, func() { cb.Data = "update_cancel:test"; s.resolveUpdate(cb, false) }, func() bool {
			pendingUpdates.Lock()
			defer pendingUpdates.Unlock()
			_, ok := pendingUpdates.items["test"]
			return ok
		}},
		{"routing", func() {
			pendingRoutingChanges.Lock()
			defer pendingRoutingChanges.Unlock()
			pendingRoutingChanges.items["test"] = pendingRoutingChange{ChatID: chat.ID, AdminID: 17, Expires: expires}
		}, func() { cb.Data = "routing_cancel:test"; s.resolveRoutingChange(cb, false) }, func() bool {
			pendingRoutingChanges.Lock()
			defer pendingRoutingChanges.Unlock()
			_, ok := pendingRoutingChanges.items["test"]
			return ok
		}},
		{"service", func() {
			pendingServiceActions.Lock()
			defer pendingServiceActions.Unlock()
			pendingServiceActions.items["test"] = pendingServiceAction{chatID: chat.ID, adminID: 17, expires: expires}
		}, func() { cb.Data = "service_confirm:test"; s.resolveServiceAction(cb) }, func() bool {
			pendingServiceActions.Lock()
			defer pendingServiceActions.Unlock()
			_, ok := pendingServiceActions.items["test"]
			return ok
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare()
			cb.From.ID = 18
			tc.invoke()
			if !tc.exists() || !strings.HasSuffix(capture.lastMethod(), "/sendMessage") {
				t.Fatal("another admin consumed or replaced the confirmation")
			}
			cb.From.ID = 17
			tc.invoke()
			if tc.exists() {
				t.Fatal("owner could not resolve their confirmation")
			}
		})
	}
}

func TestWizardOwnerCannotBeDisplaced(t *testing.T) {
	cfg := DefaultConfig()
	s, capture := newCapturedRoutingBot(t, cfg)
	chat := &tgbotapi.Chat{ID: 9543, Type: "private"}
	msg := &tgbotapi.Message{Chat: chat, From: &tgbotapi.User{ID: 18}, Text: "/cancel", Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 7}}}
	expires := time.Now().Add(time.Minute)
	tests := []struct {
		name    string
		prepare func()
		invoke  func() bool
		exists  func() bool
	}{
		{"node", func() {
			nodePathWizards.Lock()
			defer nodePathWizards.Unlock()
			nodePathWizards.items[chat.ID] = nodePathWizard{AdminID: 17, Expires: expires}
		}, func() bool { return s.handleNodePathInput(msg) }, func() bool {
			nodePathWizards.Lock()
			defer nodePathWizards.Unlock()
			_, ok := nodePathWizards.items[chat.ID]
			return ok
		}},
		{"node create", func() {
			nodeCreateStates.Lock()
			defer nodeCreateStates.Unlock()
			nodeCreateStates.items[chat.ID] = nodeCreateState{AdminID: 17, Expires: expires}
		}, func() bool { return s.handleNodeCreateInput(msg) }, func() bool {
			nodeCreateStates.Lock()
			defer nodeCreateStates.Unlock()
			_, ok := nodeCreateStates.items[chat.ID]
			return ok
		}},
		{"limit", func() {
			userLimitWizards.Lock()
			defer userLimitWizards.Unlock()
			userLimitWizards.items[chat.ID] = userLimitWizard{AdminID: 17, Expires: expires}
		}, func() bool { return s.handleUserLimitInput(msg) }, func() bool {
			userLimitWizards.Lock()
			defer userLimitWizards.Unlock()
			_, ok := userLimitWizards.items[chat.ID]
			return ok
		}},
		{"user", func() {
			managementWizards.Lock()
			defer managementWizards.Unlock()
			managementWizards.states[chat.ID] = managementWizard{AdminID: 17, Expires: expires}
		}, func() bool { return s.handleWizardMessage(msg) }, func() bool {
			managementWizards.Lock()
			defer managementWizards.Unlock()
			_, ok := managementWizards.states[chat.ID]
			return ok
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare()
			msg.From.ID = 18
			if tc.invoke() || !tc.exists() {
				t.Fatal("another admin cancelled the wizard")
			}
			msg.From.ID = 17
			if !tc.invoke() || tc.exists() {
				t.Fatal("owner could not cancel the wizard")
			}
		})
	}
	nodeCreateStates.Lock()
	nodeCreateStates.items[chat.ID] = nodeCreateState{AdminID: 17, Expires: expires, Step: "cert_source"}
	nodeCreateStates.Unlock()
	cb := &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 18}, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "node_cert_source:existing"}
	s.handleNodeCertSource(cb)
	nodeCreateStates.Lock()
	state, ok := nodeCreateStates.items[chat.ID]
	nodeCreateStates.Unlock()
	if !ok || state.Step != "cert_source" || !strings.HasSuffix(capture.lastMethod(), "/sendMessage") {
		t.Fatal("another admin interrupted certificate selection")
	}
	clearNodeCreateState(chat.ID)
}
