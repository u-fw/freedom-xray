package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func nativeNetworkExportFixture(t *testing.T) map[string]any {
	t.Helper()
	root := agentFixtureRoot(t)
	root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"tag": "proxy", "protocol": "trojan", "settings": map[string]any{"address": "proxy.example.com", "port": json.Number("443"), "password": "original-export-secret"}})
	root["dns"] = map[string]any{"servers": []any{"https://dns.example.com/query?token=original-export-secret"}}
	root["routing"].(map[string]any)["rules"] = []any{map[string]any{"type": "field", "domain": []any{"domain:example.com"}, "outboundTag": "direct", "customExtension": "original-export-secret"}}
	root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool", "selector": []any{"direct"}, "strategy": map[string]any{"type": "random", "settings": map[string]any{"customExtension": "original-export-secret"}}}}
	root["observatory"] = map[string]any{"subjectSelector": []any{"direct"}, "probeInterval": "1m", "probeURL": "https://probe.example.com/check?token=original-export-secret"}
	return root
}

func networkExportReference(t *testing.T, root map[string]any, kind string) (int, string) {
	t.Helper()
	switch kind {
	case "balancer":
		records, err := balancerRecords(root)
		if err != nil {
			t.Fatal(err)
		}
		return 1, records[0].ETag
	case "monitor":
		etag, err := routingRuleETag(root["observatory"].(map[string]any))
		if err != nil {
			t.Fatal(err)
		}
		return 1, etag
	case "dns":
		etag, err := dnsETag(root)
		if err != nil {
			t.Fatal(err)
		}
		return 0, etag
	case "rule":
		rules, _ := routingRules(root)
		etag, err := routingRuleETag(rules[0].(map[string]any))
		if err != nil {
			t.Fatal(err)
		}
		return 1, etag
	default:
		outbound := root["outbounds"].([]any)[2].(map[string]any)
		etag, err := routingRuleETag(outbound)
		if err != nil {
			t.Fatal(err)
		}
		return 3, etag
	}
}

func TestNativeNetworkExportsAreRedactedUntilConfirmedAndRejectStaleObjects(t *testing.T) {
	for _, kind := range []string{"outbound", "dns", "rule", "balancer", "monitor"} {
		t.Run(kind, func(t *testing.T) {
			root := nativeNetworkExportFixture(t)
			fixture, _ := json.Marshal(root)
			h := newBindingMigrationHarness(t, string(fixture), false)
			cfg := DefaultConfig()
			cfg.XrayConfigPath, cfg.AllowSensitiveBackup = h.config, true
			s, capture := newNativeDocumentBot(t, cfg, nil)
			index, etag := networkExportReference(t, root, kind)
			switch kind {
			case "balancer":
				s.sendBalancerDetail(9580, 1, userCallbackKey("pool"), etag)
			case "monitor":
				s.sendObservatoryDetail(9580, 1, "observatory", etag)
			case "outbound":
				s.sendOutboundDetail(9580, 1, index, etag)
			case "dns":
				s.sendDNSPage(9580, 1, 0, "")
			case "rule":
				s.sendRoutingRuleDetail(9580, 1, routingRuleRef{Index: index, ETag: etag})
			}
			capture.mu.Lock()
			markup, text, sent := capture.markup, capture.text, len(capture.documents)
			capture.mu.Unlock()
			var keyboard tgbotapi.InlineKeyboardMarkup
			if err := json.Unmarshal([]byte(markup), &keyboard); err != nil {
				t.Fatal(err)
			}
			export := nativeConfigExportCallback(kind, index, etag)
			if !strings.Contains(strings.Join(keyboardCallbacks(t, keyboard), " "), export) || strings.Contains(text, "original-export-secret") || sent != 0 {
				t.Fatal("detail omitted the export button or exposed a secret")
			}
			chat, admin := &tgbotapi.Chat{ID: 9580, Type: "private"}, &tgbotapi.User{ID: 17}
			cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: export}
			s.handleCallback(cb)
			confirm := nativeExportCallback(t, capture)
			switch kind {
			case "balancer":
				_, item, _ := findBalancer(root, "pool")
				item["strategy"].(map[string]any)["settings"] = map[string]any{"customExtension": "changed-export-secret"}
			case "monitor":
				root["observatory"].(map[string]any)["probeURL"] = "https://probe.example.com/check?token=changed-export-secret"
			case "outbound":
				root["outbounds"].([]any)[2].(map[string]any)["settings"].(map[string]any)["password"] = "changed-export-secret"
			case "dns":
				root["dns"].(map[string]any)["servers"] = []any{"https://dns.example.com/query?token=changed-export-secret"}
			case "rule":
				rules, _ := routingRules(root)
				rules[0].(map[string]any)["customExtension"] = "changed-export-secret"
			}
			changed, _ := json.Marshal(root)
			if err := os.WriteFile(h.config, changed, 0600); err != nil {
				t.Fatal(err)
			}
			cb.Data = confirm
			s.handleCallback(cb)
			capture.mu.Lock()
			sent = len(capture.documents)
			capture.mu.Unlock()
			if sent != 0 {
				t.Fatal("stale confirmation exported a changed object")
			}
			index, etag = networkExportReference(t, root, kind)
			cb.Data = nativeConfigExportCallback(kind, index, etag)
			s.handleCallback(cb)
			cb.Data = nativeExportCallback(t, capture)
			s.handleCallback(cb)
			capture.mu.Lock()
			var payload []byte
			if len(capture.documents) == 1 {
				payload = capture.documents[0]
			}
			text = capture.text
			capture.mu.Unlock()
			if !bytes.Contains(payload, []byte("changed-export-secret")) || bytes.Contains(payload, []byte(`"inbounds"`)) || strings.Contains(text, "changed-export-secret") {
				t.Fatal("confirmed export was not scoped or leaked its value into the card")
			}
			s.handleCallback(cb)
			capture.mu.Lock()
			sent = len(capture.documents)
			capture.mu.Unlock()
			if sent != 1 || h.restartCount(t) != 0 {
				t.Fatal("export was replayed or restarted Xray")
			}
		})
	}
}

func TestAgentCanExportOneRoutingRuleWithSensitiveOptIn(t *testing.T) {
	root := nativeNetworkExportFixture(t)
	fixture, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(fixture), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	index, etag := networkExportReference(t, root, "rule")
	input, _ := json.Marshal(routingRuleRef{Index: index, ETag: etag})
	var output bytes.Buffer
	if code := runAgentCLI([]string{"routing.rule.get"}, bytes.NewReader(input), &output); code != 3 || strings.Contains(output.String(), "original-export-secret") {
		t.Fatalf("single-rule export was not gated: %s", output.String())
	}
	op, _ := findAgentOperation("routing.rule.get")
	result, err := executeAgentOperation(cfg, op, input, false, true)
	if err != nil || result.(map[string]any)["customExtension"] != "original-export-secret" {
		t.Fatalf("single-rule export omitted its native extensions: %v", err)
	}
	if _, err := nativeConfigExportObject(root, "all", 0, etag); err == nil {
		t.Fatal("unknown export kind exposed the root configuration")
	}
}

func TestNativeConfigurationExportIsBoundToChatAndExpiry(t *testing.T) {
	root := nativeNetworkExportFixture(t)
	fixture, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(fixture), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.AllowSensitiveBackup = h.config, true
	s, capture := newNativeDocumentBot(t, cfg, nil)
	index, etag := networkExportReference(t, root, "dns")
	cb := &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 17}, Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 9581, Type: "private"}}, Data: nativeConfigExportCallback("dns", index, etag)}
	s.handleCallback(cb)
	confirm := nativeExportCallback(t, capture)
	cb.Data = confirm
	cb.Message.Chat = &tgbotapi.Chat{ID: 9582, Type: "private"}
	s.handleCallback(cb)
	key := strings.TrimPrefix(confirm, "ni:exconfirm:")
	pendingNativeConfigExports.Lock()
	pending, exists := pendingNativeConfigExports.items[key]
	if exists {
		pending.Expires = time.Now().Add(-time.Second)
		pendingNativeConfigExports.items[key] = pending
	}
	pendingNativeConfigExports.Unlock()
	if !exists {
		t.Fatal("another chat consumed the sensitive export confirmation")
	}
	cb.Message.Chat = &tgbotapi.Chat{ID: 9581, Type: "private"}
	s.handleCallback(cb)
	capture.mu.Lock()
	sent := len(capture.documents)
	capture.mu.Unlock()
	if sent != 0 {
		t.Fatal("another chat or an expired confirmation exported the configuration")
	}
}
