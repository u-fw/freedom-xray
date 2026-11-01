package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestNativeInboundCRUDPreservesProtectedData(t *testing.T) {
	root := agentFixtureRoot(t)
	list, _ := nativeInboundListETag(root)
	inbound := map[string]any{"tag": "vmess-node", "protocol": "vmess", "listen": "127.0.0.1", "port": json.Number("31001"), "settings": map[string]any{"clients": []any{map[string]any{"id": "11111111-1111-4111-8111-111111111111", "email": "vmess-user"}}}, "streamSettings": map[string]any{"network": "tcp"}}
	if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "add", ListETag: list, Inbound: inbound}, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	records, err := nativeInboundRecords(root)
	if err != nil || len(records) != 2 || records[1].ClientCount == nil || *records[1].ClientCount != 1 {
		t.Fatalf("invalid inbound summaries: %+v %v", records, err)
	}
	encoded, _ := json.Marshal(records)
	if bytes.Contains(encoded, []byte("11111111")) || bytes.Contains(encoded, []byte("vmess-user")) {
		t.Fatalf("credentials or identities exposed: %s", encoded)
	}
	change := nativeInboundChange{Kind: "port", Index: 2, ETag: records[1].ETag, Value: "31002"}
	if _, err := applyNativeInboundChange(root, change, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	if _, err := applyNativeInboundChange(root, change, "127.0.0.1:10085"); !errors.Is(err, errNativeInboundChanged) {
		t.Fatalf("accepted stale inbound edit: %v", err)
	}
	settings := inbound["settings"].(map[string]any)
	if settings["clients"].([]any)[0].(map[string]any)["id"] != "11111111-1111-4111-8111-111111111111" || inbound["streamSettings"].(map[string]any)["network"] != "tcp" {
		t.Fatal("listener edit lost native settings")
	}
	records, _ = nativeInboundRecords(root)
	if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "replace", Index: 2, ETag: records[1].ETag, Inbound: map[string]any{"tag": "other", "protocol": "vmess"}}, "127.0.0.1:10085"); err == nil {
		t.Fatal("replacement changed an inbound tag")
	}
	if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "replace", Index: 2, ETag: records[1].ETag, Inbound: map[string]any{"tag": []any{"vmess-node"}, "protocol": "vmess"}}, "127.0.0.1:10085"); err == nil {
		t.Fatal("accepted a non-string tag in native replacement")
	}
	if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "port", Index: 1, ETag: records[0].ETag, Value: "31003"}, "127.0.0.1:10085"); err == nil {
		t.Fatal("modified a managed VLESS node through native controls")
	}
}

func TestNativeInboundProtectsAPIRoutesAndUntaggedDeletion(t *testing.T) {
	root := agentFixtureRoot(t)
	root["inbounds"] = append(root["inbounds"].([]any), map[string]any{"tag": "custom-api", "protocol": "dokodemo-door", "listen": "127.0.0.1", "port": json.Number("10086"), "settings": map[string]any{"address": "127.0.0.1"}}, map[string]any{"protocol": "socks", "listen": "127.0.0.1", "port": json.Number("31001")})
	root["api"] = map[string]any{"tag": "api", "services": []any{"StatsService"}}
	root["routing"].(map[string]any)["rules"] = []any{map[string]any{"type": "field", "inboundTag": []any{"custom-api"}, "outboundTag": "api"}}
	records, _ := nativeInboundRecords(root)
	if !records[1].Protected {
		t.Fatal("custom API listener was not protected")
	}
	if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "port", Index: 2, ETag: records[1].ETag, Value: "31002"}, "127.0.0.1:10085"); err == nil {
		t.Fatal("changed an API listener")
	}
	if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "add", ListETag: records[0].ListETag, Inbound: map[string]any{"tag": "proxy", "protocol": "socks", "port": json.Number("31003")}}, "127.0.0.1:10085"); err == nil {
		t.Fatal("modified a config with an unsafe API route")
	}
	// A fresh configuration can delete an untagged inbound using both references.
	root = agentFixtureRoot(t)
	root["inbounds"] = append(root["inbounds"].([]any), map[string]any{"protocol": "socks", "listen": "127.0.0.1", "port": json.Number("31001")})
	records, _ = nativeInboundRecords(root)
	change := nativeInboundChange{Kind: "delete", Index: 2, ETag: records[1].ETag, ListETag: records[1].ListETag}
	if _, err := applyNativeInboundChange(root, change, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	if _, err := applyNativeInboundChange(root, change, "127.0.0.1:10085"); !errors.Is(err, errNativeInboundChanged) {
		t.Fatalf("accepted a stale untagged deletion: %v", err)
	}
}

func TestNativeInboundRealXrayProtocols(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	root := agentFixtureRoot(t)
	for index, item := range []map[string]any{
		{"tag": "vmess-test", "protocol": "vmess", "settings": map[string]any{"clients": []any{map[string]any{"id": "11111111-1111-4111-8111-111111111111", "email": "vmess-user"}}}},
		{"tag": "trojan-test", "protocol": "trojan", "settings": map[string]any{"clients": []any{map[string]any{"password": "dummy-password", "email": "trojan-user"}}}},
		{"tag": "socks-test", "protocol": "socks", "settings": map[string]any{"auth": "password", "accounts": []any{map[string]any{"user": "test", "pass": "dummy-password"}}}},
		{"tag": "ss-test", "protocol": "shadowsocks", "settings": map[string]any{"method": "aes-128-gcm", "password": "dummy-password"}},
	} {
		item["listen"], item["port"] = "127.0.0.1", json.Number(fmt.Sprint(31001+index))
		etag, _ := nativeInboundListETag(root)
		if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "add", ListETag: etag, Inbound: item}, "127.0.0.1:10085"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(root)
	if output, err := exec.Command(bin, "run", "-test", "-config", writeRoutingConfig(t, string(data))).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected native inbounds: %v\n%s", err, output)
	}
}

func TestNativeInboundBotCreateEditAndDelete(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgChat{ID: 9551, Type: "private"}, &tgUser{ID: 17}
	cb := &tgCallback{From: admin, Message: &tgMessage{MessageID: 1, Chat: chat}}
	root, _ := decodeBindingMigrationConfig(h.config)
	list, _ := nativeInboundListETag(root)
	cb.Data = "ni:add:" + list
	s.handleNativeInboundCallback(cb)
	input := `{"tag":"trojan-node","protocol":"trojan","listen":"127.0.0.1","port":31001,"settings":{"clients":[{"email":"tr-user","password":"test-secret-value"}]}}`
	if !s.handleNativeInboundInput(&tgMessage{From: admin, Chat: chat, Text: input}) {
		t.Fatal("native inbound creation was not handled")
	}
	body, _ := capture.card(t)
	if strings.Contains(body, "test-secret-value") || strings.Contains(body, "tr-user") {
		t.Fatal("confirmation exposed native credentials")
	}
	before, _ := os.ReadFile(h.config)
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgUser{ID: 18}
	s.resolveRoutingChange(cb, true)
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("another admin confirmed native inbound creation")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	records, _ := nativeInboundRecords(root)
	if len(records) != 2 || records[1].Protocol != "trojan" || h.restartCount(t) != 1 {
		t.Fatalf("inbound not created: %+v", records)
	}
	s.sendNativeInboundDetail(chat.ID, 1, 2, records[1].ETag)
	body, markup := capture.card(t)
	if strings.Contains(body, "test-secret-value") || !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "ni:port:") {
		t.Fatal("native inbound detail leaked credentials or lacks controls")
	}
	cb.Data = nativeInboundCallback("port", 2, records[1].ETag)
	s.handleNativeInboundCallback(cb)
	if !s.handleNativeInboundInput(&tgMessage{From: admin, Chat: chat, Text: "31002"}) {
		t.Fatal("native port input was not handled")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	records, _ = nativeInboundRecords(root)
	if records[1].Port != "31002" || h.restartCount(t) != 2 {
		t.Fatalf("native port not changed: %+v", records)
	}
	cb.Data = nativeInboundCallback("delete", 2, records[1].ETag)
	s.handleNativeInboundCallback(cb)
	token := pendingRoutingToken(t, chat.ID)
	root["inbounds"].([]any)[1].(map[string]any)["port"] = json.Number("31003")
	data, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	cb.Data = "routing_confirm:" + token
	s.resolveRoutingChange(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(data, after) || h.restartCount(t) != 2 {
		t.Fatal("stale native inbound deletion was applied")
	}
	root, _ = decodeBindingMigrationConfig(h.config)
	records, _ = nativeInboundRecords(root)
	cb.Data = nativeInboundCallback("delete", 2, records[1].ETag)
	s.handleNativeInboundCallback(cb)
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	if len(root["inbounds"].([]any)) != 1 || h.restartCount(t) != 3 {
		t.Fatal("native inbound was not deleted")
	}
}

func TestAgentNativeInboundPreviewAndReviewedRemoval(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	root, _ := decodeBindingMigrationConfig(h.config)
	list, _ := nativeInboundListETag(root)
	add, _ := findAgentOperation("inbounds.add")
	input, _ := json.Marshal(map[string]any{"list_etag": list, "inbound": map[string]any{"tag": "proxy", "protocol": "socks", "listen": "127.0.0.1", "port": 31001, "settings": map[string]any{"auth": "password", "accounts": []any{map[string]any{"user": "dummy", "pass": "secret-value"}}}}})
	before, _ := os.ReadFile(h.config)
	preview, err := executeAgentOperation(cfg, add, input, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(preview)
	after, _ := os.ReadFile(h.config)
	if bytes.Contains(encoded, []byte("secret-value")) || !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("native inbound preview leaked credentials or changed configuration")
	}
	if _, err := executeAgentOperation(cfg, add, input, true, false); err != nil {
		t.Fatal(err)
	}
	root, _ = decodeBindingMigrationConfig(h.config)
	records, _ := nativeInboundRecords(root)
	remove, _ := findAgentOperation("inbounds.remove")
	request := map[string]any{"index": 2, "etag": records[1].ETag, "list_etag": records[1].ListETag}
	input, _ = json.Marshal(request)
	preview, err = executeAgentOperation(cfg, remove, input, false, false)
	if err != nil {
		t.Fatal(err)
	}
	report, ok := preview.(map[string]any)["preview"].(nodeDeletionReport)
	if !ok || len(report.ETag) != 24 {
		t.Fatalf("missing deletion impact: %#v", preview)
	}
	if _, err := executeAgentOperation(cfg, remove, input, true, false); err == nil {
		t.Fatal("tagged deletion applied without a reviewed impact reference")
	}
	request["impact_etag"] = report.ETag
	input, _ = json.Marshal(request)
	if _, err := executeAgentOperation(cfg, remove, input, true, false); err != nil {
		t.Fatal(err)
	}
	root, _ = decodeBindingMigrationConfig(h.config)
	if len(root["inbounds"].([]any)) != 1 || h.restartCount(t) != 2 {
		t.Fatal("agent did not remove reviewed inbound")
	}
}

func TestNonVLESSAccountsAreNotVLESSUserBindings(t *testing.T) {
	root := agentFixtureRoot(t)
	root["inbounds"] = append(root["inbounds"].([]any), map[string]any{"tag": "vmess-node", "protocol": "vmess", "port": json.Number("31001"), "settings": map[string]any{"clients": []any{map[string]any{"id": "22222222-2222-4222-8222-222222222222", "email": "alice"}, map[string]any{"id": "33333333-3333-4333-8333-333333333333", "email": "not-managed"}}}})
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	users, err := agentUsers(h.config)
	if err != nil || len(users) != 1 || users[0].User != "alice" || len(users[0].Nodes) != 1 || users[0].Nodes[0] != "raw" || !users[0].TrafficAmbiguous {
		t.Fatalf("non-VLESS account appeared as a managed binding: %+v %v", users, err)
	}
	nodes, err := LoadXrayInbounds(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedEmails(nodes); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("non-VLESS user appeared in VLESS user list: %v", got)
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	s.sendNodesCard(9552, 1)
	body, markup := capture.card(t)
	if !strings.Contains(body, "Accounts are managed in native settings") || !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "ni:v:") {
		t.Fatalf("non-VLESS node is not directly inspectable: %s", body)
	}
	s.sendUserManageCard(9552, 1, userCallbackKey("alice"))
	_, markup = capture.card(t)
	if strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), userCallbackKey("vmess-node")) {
		t.Fatal("VLESS user card offered a non-VLESS binding")
	}
}
