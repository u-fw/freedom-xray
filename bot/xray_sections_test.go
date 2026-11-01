package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func sectionRef(t *testing.T, root map[string]any, name, kind string, value any) xraySectionChange {
	t.Helper()
	record, err := xraySectionInfo(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return xraySectionChange{Kind: kind, Name: name, ETag: record.ETag, Value: value}
}

func TestXraySectionPolicyRetainsRequiredCountersAndResetsCustomValues(t *testing.T) {
	root := agentFixtureRoot(t)
	value, err := decodeXraySectionValue("policy", []byte(`{"Levels":{"03":{"handshake":7,"StatsUserUplink":false}},"System":{"statsOutboundDownlink":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := applyXraySectionChange(root, sectionRef(t, root, "policy", "put", value))
	if err != nil || preview["required_statistics_retained"] != true {
		t.Fatalf("policy did not report retained statistics: %v %v", preview, err)
	}
	policy := root["policy"].(map[string]any)
	level := policy["levels"].(map[string]any)["3"].(map[string]any)
	if level["handshake"] != json.Number("7") || level["statsUserUplink"] != true || level["statsUserDownlink"] != true || level["statsUserOnline"] != true || policy["system"].(map[string]any)["statsOutboundDownlink"] != true {
		t.Fatalf("custom values or required counters lost: %v", policy)
	}
	if _, err := applyXraySectionChange(root, sectionRef(t, root, "policy", "reset", nil)); err != nil {
		t.Fatal(err)
	}
	policy = root["policy"].(map[string]any)
	level = policy["levels"].(map[string]any)["3"].(map[string]any)
	if level["handshake"] != nil || level["statsUserDownlink"] != true || policy["system"].(map[string]any)["statsOutboundDownlink"] != nil {
		t.Fatalf("policy reset retained custom settings or removed required counters: %v", policy)
	}
}

func TestXraySectionsPreserveOtherConfigurationAndCheckAliases(t *testing.T) {
	root := agentFixtureRoot(t)
	original, _ := json.Marshal(root["inbounds"])
	root["fakeDns"] = map[string]any{"ipPool": "198.18.0.0/24", "poolSize": json.Number("128")}
	change := sectionRef(t, root, "fakedns", "put", []any{map[string]any{"ipPool": "198.19.0.0/24", "poolSize": json.Number("64")}})
	if _, err := applyXraySectionChange(root, change); err != nil || root["fakeDns"] != nil {
		t.Fatalf("did not normalize a unique FakeDNS alias: %v", err)
	}
	if _, err := applyXraySectionChange(root, change); !errors.Is(err, errXraySectionChanged) {
		t.Fatalf("accepted stale section replacement: %v", err)
	}
	after, _ := json.Marshal(root["inbounds"])
	if !bytes.Equal(original, after) {
		t.Fatal("section edit changed inbound credentials")
	}
	root["FakeDNS"] = map[string]any{}
	if _, err := xraySectionRecords(root); err == nil {
		t.Fatal("accepted ambiguous case variants of one section")
	}
}

func TestXraySectionRejectsMalformedAndAmbiguousValues(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"log", `[]`}, {"log", `{"log":{"loglevel":"warning"}}`},
		{"fakedns", `[]`}, {"fakedns", `[null]`}, {"fakedns", `{"poolSize":1,"PoolSize":2}`},
		{"fakedns", `{"ipPool":"198.18.0.0/15"}`}, {"fakedns", `{"ipPool":"198.18.0.0/24","poolSize":256}`},
		{"fakedns", `{"ipPool":"invalid","poolSize":64}`},
		{"policy", `{"levels":[]}`}, {"policy", `{"levels":{"0":null}}`},
		{"policy", `{"levels":{"3":{},"03":{}}}`}, {"policy", `{"Levels":{},"levels":{}}`},
		{"policy", `{"levels":{"0":{"statsUserOnline":"yes"}}}`},
		{"policy", `{"system":{"statsInboundDownlink":"yes"}}`},
		{"api", `{}`},
	} {
		if _, err := decodeXraySectionValue(tc.name, []byte(tc.input)); err == nil {
			t.Fatalf("accepted malformed section %s: %s", tc.name, tc.input)
		}
	}
}

func TestAgentXraySectionPreviewIsReadOnlyAndRejectedCandidateDoesNotWrite(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, true)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	root, _ := decodeBindingMigrationConfig(h.config)
	record, _ := xraySectionInfo(root, "log")
	input, _ := json.Marshal(map[string]any{"name": "log", "etag": record.ETag, "value": map[string]any{"access": "/private/test-log-path", "loglevel": "warning"}})
	op, _ := findAgentOperation("xray.section.put")
	before, _ := os.ReadFile(h.config)
	preview, err := executeAgentOperation(cfg, op, input, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(preview)
	if bytes.Contains(encoded, []byte("test-log-path")) || string(mustReadFile(t, h.config)) != string(before) || h.restartCount(t) != 0 {
		t.Fatal("preview exposed section contents or changed configuration")
	}
	if _, err := executeAgentOperation(cfg, op, input, true, false); err == nil || string(mustReadFile(t, h.config)) != string(before) || h.restartCount(t) != 0 {
		t.Fatal("rejected candidate changed configuration")
	}
	var output bytes.Buffer
	if code := runAgentCLI([]string{"xray.section.get"}, bytes.NewReader(input), &output); code != 3 {
		t.Fatal("section export was not gated by sensitive opt-in")
	}
}

func TestBotXraySectionDocumentAndArrayExport(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations, cfg.AllowSensitiveBackup = h.config, h.xray, true, true
	payload := []byte(`[{"ipPool":"198.18.0.0/24","poolSize":128}]`)
	s, capture := newNativeDocumentBot(t, cfg, payload)
	chat, admin := &tgbotapi.Chat{ID: 9590, Type: "private"}, &tgbotapi.User{ID: 17}
	s.sendToolsCard(chat.ID, 1)
	capture.mu.Lock()
	markup := capture.markup
	capture.mu.Unlock()
	if !strings.Contains(markup, "xs:page") {
		t.Fatal("advanced settings are not reachable from the tools menu")
	}
	root, _ := decodeBindingMigrationConfig(h.config)
	record, _ := xraySectionInfo(root, "fakedns")
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: xraySectionCallback("put", "fakedns", record.ETag)}
	s.handleCallback(cb)
	if !s.handleRoutingInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "[]"}) {
		t.Fatal("invalid input was not handled")
	}
	if !s.handleRoutingInput(&tgbotapi.Message{From: admin, Chat: chat, Document: &tgbotapi.Document{FileID: "test-file"}}) {
		t.Fatal("section document was not handled after retry")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgbotapi.User{ID: 18}
	s.resolveRoutingChange(cb, true)
	if h.restartCount(t) != 0 {
		t.Fatal("another admin confirmed a section edit")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	record, _ = xraySectionInfo(root, "fakedns")
	if !record.Configured || record.Shape != "array" || h.restartCount(t) != 1 {
		t.Fatal("confirmed FakeDNS document was not applied")
	}
	cb.Data = nativeConfigExportCallback("section", xraySectionIndex("fakedns"), record.ETag)
	s.handleCallback(cb)
	cb.Data = nativeExportCallback(t, capture)
	s.handleCallback(cb)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var pools []map[string]any
	if len(capture.documents) != 1 || json.Unmarshal(capture.documents[0], &pools) != nil || len(pools) != 1 || pools[0]["ipPool"] != "198.18.0.0/24" {
		t.Fatal("array section export did not retain the native value")
	}
}

func TestXraySectionsPassInstalledCoreValidation(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	root := agentFixtureRoot(t)
	for _, tc := range []struct{ name, input string }{
		{"log", `{"access":"none","error":"none","loglevel":"warning"}`},
		{"policy", `{"levels":{"0":{"connIdle":120}}}`},
		{"fakedns", `[{"ipPool":"198.18.0.0/24","poolSize":128}]`},
	} {
		value, err := decodeXraySectionValue(tc.name, []byte(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := applyXraySectionChange(root, sectionRef(t, root, tc.name, "put", value)); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(root)
		if output, err := exec.Command(bin, "run", "-test", "-config", writeRoutingConfig(t, string(encoded))).CombinedOutput(); err != nil {
			t.Fatalf("Xray rejected %s: %v\n%s", tc.name, err, output)
		}
	}
}

func TestXraySectionSummaryShowsSettingsWithoutPrivatePathsOrExtensions(t *testing.T) {
	for name, value := range map[string]any{
		"log":     map[string]any{"loglevel": "warning", "access": "/private/log-secret", "custom": "private-extension"},
		"policy":  map[string]any{"levels": map[string]any{"0": map[string]any{"handshake": json.Number("7"), "connIdle": "private-extension", "custom": "log-secret"}}},
		"fakedns": []any{map[string]any{"ipPool": "198.18.0.0/24", "poolSize": json.Number("64"), "custom": "private-extension"}},
	} {
		encoded, _ := json.Marshal(xraySectionSafeSummary(name, value))
		if bytes.Contains(encoded, []byte("log-secret")) || bytes.Contains(encoded, []byte("private-extension")) {
			t.Fatalf("private section values exposed in %s: %s", name, encoded)
		}
		if name == "policy" && !bytes.Contains(encoded, []byte(`"handshake":7`)) {
			t.Fatalf("policy summary omitted its actual timeout: %s", encoded)
		}
		if name == "fakedns" && !bytes.Contains(encoded, []byte("198.18.0.0/24")) {
			t.Fatalf("FakeDNS summary omitted its pool: %s", encoded)
		}
	}
}

func TestXraySectionResetRejectsChangesAndRetainsExistingLogFile(t *testing.T) {
	root := agentFixtureRoot(t)
	root["log"] = map[string]any{"access": "/old/log", "loglevel": "info"}
	fixture, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(fixture), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	stale := sectionRef(t, root, "log", "reset", nil)
	logPath := filepath.Join(filepath.Dir(h.config), "existing.log")
	if err := os.WriteFile(logPath, []byte("retain this log"), 0600); err != nil {
		t.Fatal(err)
	}
	root["log"] = map[string]any{"access": logPath, "loglevel": "warning"}
	changed, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := changeXraySectionConfig(cfg, stale); !errors.Is(err, errXraySectionChanged) || string(mustReadFile(t, h.config)) != string(changed) || h.restartCount(t) != 0 {
		t.Fatalf("stale section reset was applied: %v", err)
	}
	if _, err := changeXraySectionConfig(cfg, sectionRef(t, root, "log", "reset", nil)); err != nil {
		t.Fatal(err)
	}
	if string(mustReadFile(t, logPath)) != "retain this log" {
		t.Fatal("reset deleted or changed the existing log file")
	}
	updated, _ := decodeBindingMigrationConfig(h.config)
	if _, exists := updated["log"]; exists || h.restartCount(t) != 1 {
		t.Fatal("log reset did not remove only the explicit configuration")
	}
}
