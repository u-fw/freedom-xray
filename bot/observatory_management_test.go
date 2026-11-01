package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestObservatoryCRUDAndProtectedUse(t *testing.T) {
	root := editableRoutingRoot(t)
	config := map[string]any{"subjectSelector": []any{"direct"}, "probeURL": "https://example.test/?token=hidden", "probeInterval": "1m"}
	if _, err := applyObservatoryChange(root, observatoryChange{Kind: "observatory", Action: "put", Config: config}); err != nil {
		t.Fatal(err)
	}
	records, err := observatoryRecords(root, false)
	if err != nil || len(records) != 2 || !records[0].Configured {
		t.Fatalf("records: %#v %v", records, err)
	}
	encoded, _ := json.Marshal(records)
	if bytes.Contains(encoded, []byte("token=hidden")) || !bytes.Contains(encoded, []byte("redacted_fields")) {
		t.Fatalf("probe credentials leaked: %s", encoded)
	}
	etag := records[0].ETag
	if _, err := applyObservatoryChange(root, observatoryChange{Kind: "observatory", Action: "selectors", ETag: etag, Selectors: []any{"block"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyObservatoryChange(root, observatoryChange{Kind: "observatory", Action: "delete", ETag: etag}); !errors.Is(err, errObservatoryChanged) {
		t.Fatalf("stale delete accepted: %v", err)
	}
	records, _ = observatoryRecords(root, true)
	if records[0].Config["probeURL"] != "https://example.test/?token=hidden" {
		t.Fatal("selector edit discarded existing settings")
	}
	item := map[string]any{"tag": "pool", "selector": []any{"direct"}, "strategy": map[string]any{"type": "leastPing"}}
	if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: item}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyObservatoryChange(root, observatoryChange{Kind: "observatory", Action: "delete", ETag: records[0].ETag}); err == nil {
		t.Fatal("deleted monitor required by balancer")
	}
}

func TestObservatoryFieldEditingPreservesOtherSettings(t *testing.T) {
	for _, kind := range []string{"observatory", "burstObservatory"} {
		t.Run(kind, func(t *testing.T) {
			root := editableRoutingRoot(t)
			config := map[string]any{"subjectSelector": []any{"direct"}}
			if kind == "observatory" {
				config["probeUrl"] = "https://example.test/old"
			} else {
				config["pingConfig"] = map[string]any{"sampling": json.Number("4")}
			}
			if _, err := applyObservatoryChange(root, observatoryChange{Kind: kind, Action: "put", Config: config}); err != nil {
				t.Fatal(err)
			}
			records, _ := observatoryRecords(root, true)
			var etag string
			for _, record := range records {
				if record.Kind == kind {
					etag = record.ETag
				}
			}
			if _, err := applyObservatoryChange(root, observatoryChange{Kind: kind, Action: "field", ETag: etag, Field: "url", Value: "https://example.test/new?token=hidden"}); err != nil {
				t.Fatal(err)
			}
			records, _ = observatoryRecords(root, true)
			for _, record := range records {
				if record.Kind == kind {
					etag = record.ETag
				}
			}
			if _, err := applyObservatoryChange(root, observatoryChange{Kind: kind, Action: "field", ETag: etag, Field: "interval", Value: "30s"}); err != nil {
				t.Fatal(err)
			}
			updated, _ := readObservatory(root, kind)
			if kind == "observatory" {
				if updated["probeURL"] != "https://example.test/new?token=hidden" || updated["probeUrl"] != nil || updated["probeInterval"] != "30s" {
					t.Fatalf("wrong monitor settings: %#v", updated)
				}
			} else {
				ping, _ := getMap(updated["pingConfig"])
				if ping["sampling"] != json.Number("4") || ping["interval"] != "30s" || ping["destination"] != "https://example.test/new?token=hidden" {
					t.Fatalf("wrong burst settings: %#v", ping)
				}
			}
		})
	}
}

func TestObservatoryBotConfigurationNeedsConfirmation(t *testing.T) {
	h := newBindingMigrationHarness(t, editableRoutingFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9502, Type: "private"}, &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "mo:page"}
	s.handleObservatoryCallback(cb)
	_, markup := capture.card(t)
	keyboardCallbacks(t, markup)
	cb.Data = "mo:edit:o:new"
	s.handleObservatoryCallback(cb)
	before, _ := os.ReadFile(h.config)
	if !s.handleBalancerInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "direct"}) {
		t.Fatal("monitor selectors not handled")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("monitor changed before confirmation")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, err := decodeBindingMigrationConfig(h.config)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := readObservatory(root, "observatory")
	if err != nil || monitor == nil || h.restartCount(t) != 1 {
		t.Fatalf("monitor not created: %#v %v", monitor, err)
	}
	records, _ := observatoryRecords(root, false)
	cb.Data = observatoryCallback("url", "observatory", records[0].ETag)
	s.handleObservatoryCallback(cb)
	if !s.handleBalancerInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "https://example.test/?token=secret-value"}) {
		t.Fatal("URL input not handled")
	}
	body, _ := capture.card(t)
	if bytes.Contains([]byte(body), []byte("secret-value")) {
		t.Fatal("confirmation exposed URL credentials")
	}
}

func TestObservatoryAcceptedByRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	for _, kind := range []string{"observatory", "burstObservatory"} {
		t.Run(kind, func(t *testing.T) {
			root := agentFixtureRoot(t)
			config := map[string]any{"subjectSelector": []any{"direct"}}
			if kind == "observatory" {
				config["probeInterval"] = "1m"
			} else {
				config["pingConfig"] = map[string]any{}
			}
			if _, err := applyObservatoryChange(root, observatoryChange{Kind: kind, Action: "put", Config: config}); err != nil {
				t.Fatal(err)
			}
			records, _ := observatoryRecords(root, false)
			for _, record := range records {
				if record.Kind != kind {
					continue
				}
				if _, err := applyObservatoryChange(root, observatoryChange{Kind: kind, Action: "field", ETag: record.ETag, Field: "url", Value: "https://example.com/generate_204"}); err != nil {
					t.Fatal(err)
				}
			}
			item := map[string]any{"tag": "pool", "selector": []any{"direct"}, "strategy": map[string]any{"type": "leastPing"}}
			if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: item}); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(root)
			path := writeRoutingConfig(t, string(data))
			if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
				t.Fatalf("Xray rejected %s: %v\n%s", kind, err, output)
			}
		})
	}
}
