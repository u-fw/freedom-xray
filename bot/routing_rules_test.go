package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const editableRoutingFixture = `{
 "inbounds":[{"tag":"api-internal","listen":"127.0.0.1","port":10085,"protocol":"dokodemo-door","settings":{"address":"127.0.0.1"}},
             {"tag":"raw","port":443,"protocol":"vless","settings":{"decryption":"none","clients":[{"id":"11111111-1111-4111-8111-111111111111","email":"alice"}]}}],
 "api":{"tag":"api","services":["StatsService"]},
 "outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"},{"tag":"api","protocol":"freedom"}],
 "routing":{"rules":[
  {"type":"field","inboundTag":["api-internal"],"outboundTag":"api"},
  {"type":"field","domain":["domain:example.com","geosite:cn"],"outboundTag":"direct","customFlag":"preserve"},
  {"type":"field","ruleTag":"deny","ip":["geoip:private"],"outboundTag":"block"}
 ]}
}`

func editableRoutingRoot(t *testing.T) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(editableRoutingFixture))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	return root
}

func routeRef(t *testing.T, root map[string]any, index int) routingRuleRef {
	t.Helper()
	records, err := routingRuleRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	return routingRuleRef{Index: index, ETag: records[index-1].ETag}
}

func TestRouteEditUnnamedPreservesUnknownAndRejectsStale(t *testing.T) {
	root := editableRoutingRoot(t)
	ref := routeRef(t, root, 2)
	change := routingRuleEdit{Ref: ref, Kind: "field", Field: "domain", Value: []any{"domain:new.example", "geosite:cn"}}
	if _, err := applyRoutingRuleEdit(root, change); err != nil {
		t.Fatal(err)
	}
	rules, _ := routingRules(root)
	rule := rules[1].(map[string]any)
	if rule["customFlag"] != "preserve" || rule["ruleTag"] != nil || rule["outboundTag"] != "direct" {
		t.Fatalf("unrelated settings changed: %#v", rule)
	}
	if _, err := applyRoutingRuleEdit(root, change); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("accepted stale edit: %v", err)
	}
}

func TestRouteEditProtectsAPIAndRuleConditions(t *testing.T) {
	root := editableRoutingRoot(t)
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: routeRef(t, root, 1), Kind: "delete"}); err == nil {
		t.Fatal("deleted API rule")
	}
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: routeRef(t, root, 2), Kind: "move", Direction: -1}); err == nil {
		t.Fatal("moved ahead of API route")
	}
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: routeRef(t, root, 2), Kind: "field", Field: "inboundTag", Value: []any{"api-internal"}}); err == nil {
		t.Fatal("accepted protected inbound")
	}
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: routeRef(t, root, 3), Kind: "field", Field: "ip", Value: nil}); err == nil {
		t.Fatal("removed the final matching condition")
	}
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: routeRef(t, root, 2), Kind: "field", Field: "domain", Value: nil}); err == nil {
		t.Fatal("unknown metadata counted as a matching condition")
	}
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: routeRef(t, root, 2), Kind: "target", Field: "outboundTag", Value: "missing"}); err == nil {
		t.Fatal("accepted unknown target")
	}
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: routeRef(t, root, 2), Kind: "target", Field: "outboundTag", Value: "api"}); err == nil {
		t.Fatal("accepted API outbound")
	}
}

func TestRouteMoveRejectsChangedNeighbor(t *testing.T) {
	root := editableRoutingRoot(t)
	ref := routeRef(t, root, 2)
	listETag, err := routingListETag(root)
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := routingRules(root)
	rules[2].(map[string]any)["ip"] = []any{"geoip:cn"}
	_, err = applyRoutingRuleEdit(root, routingRuleEdit{Ref: ref, Kind: "move", Direction: 1, ExpectedListETag: listETag})
	if !errors.Is(err, errRoutingRuleChanged) {
		t.Fatalf("moved relative to a changed neighbor: %v", err)
	}
}

func TestRoutingAliasesAndSensitiveExtensions(t *testing.T) {
	root := editableRoutingRoot(t)
	rules, _ := routingRules(root)
	rule := rules[1].(map[string]any)
	rule["domains"] = []any{"domain:effective.example"}
	rule["source"] = []any{"192.0.2.1"}
	rule["sourceIP"] = []any{"192.0.2.2"}
	rule["port"] = json.Number("443")
	rule["webhook"] = map[string]any{"url": "https://example.test/?token=private-value", "headers": map[string]any{"Authorization": "private-header"}}
	records, err := routingRuleRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	redacted := redactRoutingRuleRecords(records)
	encoded, _ := json.Marshal(redacted)
	if bytes.Contains(encoded, []byte("private-value")) || bytes.Contains(encoded, []byte("private-header")) || !bytes.Contains(encoded, []byte("redacted_fields")) {
		t.Fatalf("sensitive extension was not redacted: %s", encoded)
	}
	if redacted[1].ETag != records[1].ETag {
		t.Fatal("redaction changed edit reference")
	}
	body := routingRuleDetailText("en", records[1], root)
	if strings.Contains(body, "private-value") || strings.Contains(body, "private-header") || !strings.Contains(body, "domain:effective.example") {
		t.Fatal("Telegram detail did not redact extension or show domain alias")
	}

	h := newBindingMigrationHarness(t, editableRoutingFixture, false)
	data, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := LoadRouting(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Rules[1].Domains[0] != "domain:effective.example" || snapshot.Rules[1].SourceIPs[0] != "192.0.2.2" || snapshot.Rules[1].Ports[0] != "443" {
		t.Fatalf("routing summary ignored native aliases or numeric port: %+v", snapshot.Rules[1])
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.AllowMutations = h.config, true
	s, capture := newCapturedRoutingBot(t, cfg)
	s.sendRoutingRuleDetail(9491, 1, routeRef(t, root, 2))
	_, markup := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(callbacks, "rt:f:domains:") || !strings.Contains(callbacks, "rt:f:sourceIP:") {
		t.Fatalf("editing points to an overridden alias: %s", callbacks)
	}
	op, _ := findAgentOperation("routing.rules")
	output, err := executeAgentOperation(cfg, op, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(output)
	if bytes.Contains(encoded, []byte("private-header")) {
		t.Fatal("agent exposed a secret without opt-in")
	}
	output, err = executeAgentOperation(cfg, op, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(output)
	if !bytes.Contains(encoded, []byte("private-header")) {
		t.Fatal("sensitive opt-in did not expose the native rule")
	}
}

func TestRouteEditMovesAndDeletesExactRule(t *testing.T) {
	root := editableRoutingRoot(t)
	ref := routeRef(t, root, 3)
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: ref, Kind: "move", Direction: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := lookupRoutingRule(root, ref); err == nil {
		t.Fatal("old position remained valid")
	}
	ref = routeRef(t, root, 2)
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: ref, Kind: "delete"}); err != nil {
		t.Fatal(err)
	}
	rules, _ := routingRules(root)
	if len(rules) != 2 {
		t.Fatalf("unexpected count %d", len(rules))
	}
	if _, ok := rules[1].(map[string]any)["domain"]; !ok {
		t.Fatal("deleted the wrong rule")
	}
}

func TestRouteEditRejectsInvalidCandidateWithoutWriting(t *testing.T) {
	h := newBindingMigrationHarness(t, editableRoutingFixture, true)
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	cfg.XrayBinPath = h.xray
	root, err := decodeBindingMigrationConfig(h.config)
	if err != nil {
		t.Fatal(err)
	}
	change := routingRuleEdit{Ref: routeRef(t, root, 2), Kind: "target", Field: "outboundTag", Value: "block"}
	before, _ := os.ReadFile(h.config)
	if _, err := changeRoutingRule(cfg, change); err == nil {
		t.Fatal("invalid candidate accepted")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("failed validation changed the live configuration")
	}
}

func TestAgentCanPreviewAndApplyUnnamedRuleEdit(t *testing.T) {
	h := newBindingMigrationHarness(t, editableRoutingFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	ref := routeRef(t, editableRoutingRoot(t), 2)
	input, err := json.Marshal(map[string]any{"index": ref.Index, "etag": ref.ETag, "field": "domain", "value": []any{"domain:agent.example"}})
	if err != nil {
		t.Fatal(err)
	}
	op, ok := findAgentOperation("routing.rule.patch")
	if !ok {
		t.Fatal("agent operation not discoverable")
	}
	before, _ := os.ReadFile(h.config)
	if _, err = executeAgentOperation(cfg, op, input, false, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("agent preview changed configuration")
	}
	if _, err = executeAgentOperation(cfg, op, input, true, false); err != nil {
		t.Fatal(err)
	}
	if h.restartCount(t) != 1 {
		t.Fatal("agent apply did not restart Xray")
	}
	if _, err = executeAgentOperation(cfg, op, input, true, false); !errors.Is(err, errRoutingRuleChanged) {
		t.Fatalf("replayed agent edit: %v", err)
	}
}

func TestGuidedRoutingChangesAcceptedByRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	root := agentFixtureRoot(t)
	if _, err := applyStatsInfrastructure(root, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	change := routingChange{Kind: "add", RuleTag: "test-route", Rule: map[string]any{"type": "field", "ruleTag": "test-route", "domain": []any{"domain:example.com"}, "outboundTag": "direct"}}
	if _, err := applyGuidedRoutingAdd(root, change); err != nil {
		t.Fatal(err)
	}
	ref := routeRef(t, root, 2)
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: ref, Kind: "field", Field: "port", Value: "80,443"}); err != nil {
		t.Fatal(err)
	}
	ref = routeRef(t, root, 2)
	if _, err := applyRoutingRuleEdit(root, routingRuleEdit{Ref: ref, Kind: "target", Field: "outboundTag", Value: "block"}); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	path := writeRoutingConfig(t, string(config))
	if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected guided routing: %v\n%s", err, output)
	}
}

func TestRoutingScriptPreventsRuleAndStatsRepairChanges(t *testing.T) {
	root := agentFixtureRoot(t)
	root["routing"] = map[string]any{"script": "routing.lua"}
	before, _ := json.Marshal(root)
	change := routingChange{Kind: "add", RuleTag: "test", Rule: map[string]any{"ruleTag": "test", "domain": []any{"domain:example.com"}, "outboundTag": "direct"}}
	if _, err := applyGuidedRoutingAdd(root, change); err == nil {
		t.Fatal("accepted a rule while script controls routing")
	}
	if _, err := applyStatsInfrastructure(root, "127.0.0.1:10085"); err == nil {
		t.Fatal("stats repair attempted to change a routing script")
	}
	after, _ := json.Marshal(root)
	if !bytes.Equal(before, after) {
		t.Fatal("stats repair partially changed script configuration")
	}
	path := writeRoutingConfig(t, string(before))
	report, err := inspectStatsSwitches(path)
	if err != nil || !strings.Contains(strings.Join(report.APIMissing, ","), "routing script") {
		t.Fatalf("script warning missing: %+v %v", report, err)
	}
	snapshot, err := LoadRouting(path)
	if err != nil || !snapshot.ScriptConfigured {
		t.Fatalf("script not exposed to agent: %+v %v", snapshot, err)
	}
}
