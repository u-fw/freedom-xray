package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func rootWithNativeAccount(t *testing.T, email string) map[string]any {
	t.Helper()
	root := agentFixtureRoot(t)
	clients := root["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["clients"].([]any)
	clients = append(clients, map[string]any{"id": "22222222-2222-4222-8222-222222222222", "email": "bob"})
	root["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["clients"] = clients
	root["inbounds"] = append(root["inbounds"].([]any), map[string]any{"tag": "trojan-node", "protocol": "trojan", "listen": "127.0.0.1", "port": json.Number("31001"), "settings": map[string]any{"clients": []any{map[string]any{"email": email, "password": "dummy-password"}}}})
	return root
}

func TestNativeInboundRejectsManagedIdentityCollisions(t *testing.T) {
	for _, email := range []string{"alice", bindingEmail("alice", "another-node")} {
		root := agentFixtureRoot(t)
		list, _ := nativeInboundListETag(root)
		item := map[string]any{"tag": "trojan", "protocol": "trojan", "settings": map[string]any{"clients": []any{map[string]any{"email": email, "password": "dummy"}}}}
		if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "add", ListETag: list, Inbound: item}, "127.0.0.1:10085"); err == nil {
			t.Fatalf("accepted native account with managed identity %s", email)
		}
	}
	root := agentFixtureRoot(t)
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	if err := os.WriteFile(directoryPath(h.config), []byte(`{"users":{"unbound":{"id":"33333333-3333-4333-8333-333333333333"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	list, _ := nativeInboundListETag(root)
	item := map[string]any{"tag": "trojan", "protocol": "trojan", "settings": map[string]any{"clients": []any{map[string]any{"email": "unbound", "password": "dummy"}}}}
	if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "add", ListETag: list, Inbound: item}, "127.0.0.1:10085", h.config); err == nil {
		t.Fatal("accepted collision with an unbound managed user")
	}
	if err := os.WriteFile(suspendedStorePath(h.config), []byte(`{"users":{"parked":{"uuid":"44444444-4444-4444-8444-444444444444","nodes":{}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	item["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)["email"] = "parked"
	if _, err := applyNativeInboundChange(root, nativeInboundChange{Kind: "add", ListETag: list, Inbound: item}, "127.0.0.1:10085", h.config); err == nil {
		t.Fatal("accepted collision with a suspended managed user")
	}
}

func TestManagedUserCannotClaimNativeStatisticsIdentity(t *testing.T) {
	root := rootWithNativeAccount(t, "native-user")
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "add", User: "native-user"}); err == nil {
		t.Fatal("created managed user with an existing native statistics identity")
	}
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "rename", User: "alice", NewName: "native-user"}); err == nil {
		t.Fatal("renamed managed user onto an existing native statistics identity")
	}
}

func TestSuspendedUserCannotRestoreOntoNativeStatisticsIdentity(t *testing.T) {
	root := rootWithNativeAccount(t, "native-user")
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "disable", User: "alice"}); err != nil {
		t.Fatal(err)
	}
	root, _ = decodeBindingMigrationConfig(h.config)
	root["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)["email"] = "alice"
	data, _ = json.Marshal(root)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "enable", User: "alice"}); err == nil {
		t.Fatal("restored managed user onto a native statistics identity")
	}
	if !isSuspendedUser(h.config, "alice") {
		t.Fatal("failed restore discarded the suspended account")
	}
}

func TestRenamingManagedUserPreservesTrafficAmbiguity(t *testing.T) {
	root := rootWithNativeAccount(t, "alice")
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "rename", User: "alice", NewName: "eve"}); err != nil {
		t.Fatal(err)
	}
	if marker, err := trafficMetaValue(h.config, nativeTrafficAmbiguityPrefix+"eve"); err != nil || marker != "1" {
		t.Fatalf("renamed user lost traffic ambiguity: %q %v", marker, err)
	}
	if marker, err := trafficMetaValue(h.config, nativeTrafficAmbiguityPrefix+"alice"); err != nil || marker != "" {
		t.Fatalf("old traffic ambiguity was not moved: %q %v", marker, err)
	}
}

func TestAmbiguousQuotaStaysPausedUntilSafeUserReset(t *testing.T) {
	root := rootWithNativeAccount(t, "alice")
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	state := trafficStore{Users: map[string]trafficCounter{"alice": {TotalDown: 200}}}
	if err := saveTrafficStore(h.config, state); err != nil {
		t.Fatal(err)
	}
	if err := writeUserLimitStore(userLimitStorePath(h.config), userLimitStore{Users: map[string]userLimit{"alice": {QuotaBytes: 100}}}); err != nil {
		t.Fatal(err)
	}
	if action, _, suspended, err := recheckAndSuspendLimitedUser(h.config, h.xray, "127.0.0.1:10085", "alice", time.Now()); err != nil || suspended || action.Disable {
		t.Fatalf("ambiguous quota suspended a user: %+v %v %v", action, suspended, err)
	}
	if marker, err := trafficMetaValue(h.config, nativeTrafficAmbiguityPrefix+"alice"); err != nil || marker != "1" {
		t.Fatalf("missing persisted ambiguity marker: %q %v", marker, err)
	}
	if err := resetUserTraffic(h.config, h.xray, "127.0.0.1:10085", "alice"); err == nil {
		t.Fatal("reset shared runtime counters while identities collide")
	}
	root["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)["email"] = "native-alice"
	data, _ = json.Marshal(root)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	if action, _, suspended, err := recheckAndSuspendLimitedUser(h.config, h.xray, "127.0.0.1:10085", "alice", time.Now()); err != nil || suspended || action.Disable {
		t.Fatalf("historically ambiguous quota was resumed without reset: %+v %v %v", action, suspended, err)
	}
	users, err := agentUsers(h.config)
	if err != nil || len(users) == 0 || !users[0].TrafficAmbiguous {
		t.Fatalf("agent did not report retained ambiguity: %+v %v", users, err)
	}
	if err := resetUserTraffic(h.config, h.xray, "127.0.0.1:10085", "alice"); err != nil {
		t.Fatal(err)
	}
	if marker, err := trafficMetaValue(h.config, nativeTrafficAmbiguityPrefix+"alice"); err != nil || marker != "" {
		t.Fatalf("safe user reset did not clear ambiguity: %q %v", marker, err)
	}
	if action, used, suspended, err := recheckAndSuspendLimitedUser(h.config, h.xray, "127.0.0.1:10085", "alice", time.Now()); err != nil || suspended || action.Disable || used != 0 {
		t.Fatalf("quota did not resume with reset counters: %+v %d %v %v", action, used, suspended, err)
	}
}

func TestReplacingConflictingNativeAccountMarksOldTrafficBeforeRestart(t *testing.T) {
	root := rootWithNativeAccount(t, "alice")
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	records, _ := nativeInboundRecords(root)
	replacement := map[string]any{"tag": "trojan-node", "protocol": "trojan", "listen": "127.0.0.1", "port": json.Number("31001"), "settings": map[string]any{"clients": []any{map[string]any{"email": "native-alice", "password": "dummy-password"}}}}
	if _, err := changeNativeInboundConfig(cfg, nativeInboundChange{Kind: "replace", Index: 2, ETag: records[1].ETag, Inbound: replacement}); err != nil {
		t.Fatal(err)
	}
	marker, err := trafficMetaValue(h.config, nativeTrafficAmbiguityPrefix+"alice")
	if err != nil || marker != "1" {
		t.Fatalf("old live identity was not marked before restart: %q %v", marker, err)
	}
	if !strings.Contains(string(mustReadFile(t, h.config)), "native-alice") {
		t.Fatal("native account was not updated")
	}
}

func TestAmbiguousTrafficDoesNotPreventExpiryEnforcement(t *testing.T) {
	root := rootWithNativeAccount(t, "alice")
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	if err := saveTrafficStore(h.config, trafficStore{Users: map[string]trafficCounter{"alice": {TotalDown: 200}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeUserLimitStore(userLimitStorePath(h.config), userLimitStore{Users: map[string]userLimit{"alice": {QuotaBytes: 100, ExpiryUnix: time.Now().Unix() - 1}}}); err != nil {
		t.Fatal(err)
	}
	action, _, suspended, err := recheckAndSuspendLimitedUser(h.config, h.xray, "127.0.0.1:10085", "alice", time.Now())
	if err != nil || !suspended || action.Reason != "expired" {
		t.Fatalf("independent expiry enforcement failed: %+v %v %v", action, suspended, err)
	}
}

func TestAmbiguousQuotaNotifiesOnceAndDoesNotSuspend(t *testing.T) {
	root := rootWithNativeAccount(t, "alice")
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	if err := saveTrafficStore(h.config, trafficStore{Users: map[string]trafficCounter{"alice": {TotalDown: 200}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeUserLimitStore(userLimitStorePath(h.config), userLimitStore{Users: map[string]userLimit{"alice": {QuotaBytes: 100}}}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations, cfg.AdminIDs = h.config, h.xray, true, []int64{17}
	s, capture := newCapturedRoutingBot(t, cfg)
	s.enforceUserLimitsOnce()
	limits, err := snapshotUserLimits(h.config)
	if err != nil || !limits["alice"].NotifiedAmbiguous || h.restartCount(t) != 0 || isSuspendedUser(h.config, "alice") {
		t.Fatalf("ambiguous quota was not paused and reported: %+v %v", limits, err)
	}
	count := capture.messageCount()
	if count != 1 {
		t.Fatalf("expected one admin notification, got %d", count)
	}
	s.enforceUserLimitsOnce()
	if capture.messageCount() != count {
		t.Fatal("repeated unchanged ambiguity notification")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
