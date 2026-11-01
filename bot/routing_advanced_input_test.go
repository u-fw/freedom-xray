package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAdvancedNetworkWizardRetriesAndAcceptsJSONDocument(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	payload, _ := json.Marshal(map[string]any{"tag": "uploaded", "protocol": "freedom", "settings": map[string]any{"extension": strings.Repeat("x", 5000)}})
	s, _ := newNativeDocumentBot(t, cfg, payload)
	chat, admin := &tgChat{ID: 9565, Type: "private"}, &tgUser{ID: 17}
	cb := &tgCallback{From: admin, Message: &tgMessage{MessageID: 1, Chat: chat}, Data: "routing_manage:outbound_put"}
	s.beginRoutingWizard(cb)
	before, _ := os.ReadFile(h.config)
	if !s.handleRoutingInput(&tgMessage{From: admin, Chat: chat, Text: "{"}) {
		t.Fatal("invalid JSON was not handled")
	}
	routingWizards.Lock()
	_, retained := routingWizards.items[chat.ID]
	routingWizards.Unlock()
	if !retained {
		t.Fatal("invalid input discarded the current step")
	}
	if !s.handleRoutingInput(&tgMessage{From: admin, Chat: chat, Document: &tgDocument{FileID: "test-file"}}) {
		t.Fatal("JSON document was not handled")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("unconfirmed upload changed configuration")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ := decodeBindingMigrationConfig(h.config)
	if len(root["outbounds"].([]any)) != 3 || h.restartCount(t) != 1 {
		t.Fatal("confirmed outbound document was not applied")
	}
}

func TestAdvancedRoutingAndNetworkConfirmationsRejectStaleState(t *testing.T) {
	for _, kind := range []string{"add", "outbound_put", "dns_put"} {
		t.Run(kind, func(t *testing.T) {
			h := newBindingMigrationHarness(t, agentFixture, false)
			cfg := DefaultConfig()
			cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
			s := testRemoteBot(t, cfg)
			chat, admin := &tgChat{ID: 9570, Type: "private"}, &tgUser{ID: 17}
			cb := &tgCallback{From: admin, Message: &tgMessage{MessageID: 1, Chat: chat}, Data: "routing_manage:" + kind}
			s.beginRoutingWizard(cb)
			input := `{"ruleTag":"ads","domain":["domain:example.com"],"outboundTag":"block"}`
			if kind == "outbound_put" {
				input = `{"tag":"direct","protocol":"freedom"}`
			} else if kind == "dns_put" {
				input = `{"servers":["localhost"]}`
			}
			if !s.handleRoutingInput(&tgMessage{From: admin, Chat: chat, Text: input}) {
				t.Fatal("wizard did not accept input")
			}
			cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
			root, _ := decodeBindingMigrationConfig(h.config)
			switch kind {
			case "add":
				root["routing"].(map[string]any)["rules"] = []any{map[string]any{"type": "field", "domain": []any{"other.example"}, "outboundTag": "direct"}}
			case "outbound_put":
				root["outbounds"].([]any)[0].(map[string]any)["settings"] = map[string]any{"domainStrategy": "UseIP"}
			case "dns_put":
				root["dns"] = map[string]any{"servers": []any{"8.8.8.8"}}
			}
			changed, _ := json.Marshal(root)
			if err := os.WriteFile(h.config, changed, 0600); err != nil {
				t.Fatal(err)
			}
			s.resolveRoutingChange(cb, true)
			after, _ := os.ReadFile(h.config)
			if !bytes.Equal(changed, after) || h.restartCount(t) != 0 {
				t.Fatal("stale confirmation overwrote the changed configuration")
			}
		})
	}
}

func TestCancellingAdvancedWizardDuringDownloadPreventsConfirmation(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, transport := newNativeDocumentBot(t, cfg, []byte(`{"tag":"uploaded","protocol":"freedom"}`))
	transport.mu.Lock()
	transport.started, transport.release = make(chan struct{}, 1), make(chan struct{})
	started, release := transport.started, transport.release
	transport.mu.Unlock()
	chat, admin := &tgChat{ID: 9566, Type: "private"}, &tgUser{ID: 17}
	cb := &tgCallback{From: admin, Message: &tgMessage{MessageID: 1, Chat: chat}, Data: "routing_manage:outbound_put"}
	s.beginRoutingWizard(cb)
	done := make(chan bool, 1)
	go func() {
		done <- s.handleRoutingInput(&tgMessage{From: admin, Chat: chat, Document: &tgDocument{FileID: "test-file"}})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	clearRoutingWizard(chat.ID)
	close(release)
	select {
	case handled := <-done:
		if !handled {
			t.Fatal("document was not handled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download did not finish")
	}
	pendingRoutingChanges.Lock()
	defer pendingRoutingChanges.Unlock()
	for _, pending := range pendingRoutingChanges.items {
		if pending.ChatID == chat.ID {
			t.Fatal("cancelled wizard queued a confirmation after download")
		}
	}
}
