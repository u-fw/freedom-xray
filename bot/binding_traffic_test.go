package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrafficSnapshotDoubleWritesBindingsAndOwnerTotals(t *testing.T) {
	state := trafficStore{Users: map[string]trafficCounter{}, Bindings: map[string]trafficCounter{}, Inbounds: map[string]trafficCounter{}}
	raw := bindingEmail("alice", "raw")
	cdn := bindingEmail("alice", "cdn")
	users := []UserTraffic{
		{Email: raw, Uplink: 100, Downlink: 50},
		{Email: cdn, Uplink: 20, Downlink: 10},
		{Email: "bob", Uplink: 5, Downlink: 5},
	}
	if !applyTrafficSnapshot(&state, users, nil, "epoch-a") {
		t.Fatal("first sample did not change state")
	}
	if state.Bindings[raw].TotalUp != 100 || state.Bindings[raw].TotalDown != 50 {
		t.Fatalf("raw binding: %+v", state.Bindings[raw])
	}
	if state.Users["alice"].TotalUp != 120 || state.Users["alice"].TotalDown != 60 {
		t.Fatalf("owner aggregate: %+v", state.Users["alice"])
	}
	if state.Users["alice"].LastUp != 0 || state.Users["alice"].Epoch != "" {
		t.Fatalf("owner must remain a sum, not a runtime watermark: %+v", state.Users["alice"])
	}
	if state.Users["bob"].TotalUp != 5 {
		t.Fatalf("legacy user lost: %+v", state.Users["bob"])
	}
	if applyTrafficSnapshot(&state, users, nil, "epoch-a") {
		t.Fatal("identical sample changed state")
	}
	users[0] = UserTraffic{Email: raw, Uplink: 150, Downlink: 80}
	if !applyTrafficSnapshot(&state, users, nil, "epoch-a") {
		t.Fatal("delta sample did not change state")
	}
	if state.Bindings[raw].TotalUp != 150 || state.Bindings[raw].TotalDown != 80 {
		t.Fatalf("raw delta: %+v", state.Bindings[raw])
	}
	if state.Users["alice"].TotalUp != 170 || state.Users["alice"].TotalDown != 90 {
		t.Fatalf("owner delta: %+v", state.Users["alice"])
	}
	users[0] = UserTraffic{Email: raw, Uplink: 10, Downlink: 5}
	if !applyTrafficSnapshot(&state, users, nil, "epoch-b") {
		t.Fatal("restart sample did not change state")
	}
	if state.Bindings[raw].TotalUp != 160 || state.Bindings[raw].TotalDown != 85 {
		t.Fatalf("restart binding: %+v", state.Bindings[raw])
	}
	if state.Users["alice"].TotalUp != 200 || state.Users["alice"].TotalDown != 105 {
		t.Fatalf("restart owner: %+v", state.Users["alice"])
	}
}

func TestBindingTrafficRenameDeleteAndNodeReset(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	state := trafficStore{
		Users: map[string]trafficCounter{"alice": {TotalUp: 120, TotalDown: 60}},
		Bindings: map[string]trafficCounter{
			bindingEmail("alice", "raw"): {TotalUp: 100, TotalDown: 50, LastUp: 100, LastDown: 50, Epoch: "e"},
			bindingEmail("alice", "cdn"): {TotalUp: 20, TotalDown: 10, LastUp: 20, LastDown: 10, Epoch: "e"},
		},
	}
	if err := saveTrafficStore(config, state); err != nil {
		t.Fatal(err)
	}
	if err := renameTrafficStoreLocked(config, "alice", "bob"); err != nil {
		t.Fatal(err)
	}
	renamed, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := renamed.Users["alice"]; ok {
		t.Fatal("old owner remains")
	}
	if renamed.Users["bob"].TotalUp != 120 {
		t.Fatalf("rename lost owner history: %+v", renamed.Users["bob"])
	}
	if _, ok := renamed.Bindings[bindingEmail("alice", "raw")]; ok {
		t.Fatal("old binding remains")
	}
	moved := renamed.Bindings[bindingEmail("bob", "raw")]
	if moved.TotalUp != 100 || moved.LastUp != 0 || moved.Epoch != "" {
		t.Fatalf("binding move damaged history or watermark: %+v", moved)
	}
	if err := resetNodeBindingCounters(config, "cdn"); err != nil {
		t.Fatal(err)
	}
	reset, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Bindings[bindingEmail("bob", "cdn")].TotalUp != 20 {
		t.Fatal("node reset erased history")
	}
	if reset.Bindings[bindingEmail("bob", "cdn")].LastUp != 0 {
		t.Fatal("node reset kept stale watermark")
	}
	if !discardTrafficUser(&reset, "bob") {
		t.Fatal("delete did not change state")
	}
	if _, ok := reset.Bindings[bindingEmail("bob", "raw")]; ok {
		t.Fatal("bindings survive user deletion")
	}
}

func TestResetUserTrafficClearsEveryBindingAlias(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	logPath := filepath.Join(dir, "args.log")
	bin := filepath.Join(dir, "xray")
	mock := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\nexit 0\n"
	if err := os.WriteFile(bin, []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	root := testConfig(t)
	if _, err := applyManagedUserChange(root, UserChange{Kind: "add", User: "phone", Node: "all"}); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	state := trafficStore{
		Users: map[string]trafficCounter{"phone": {TotalUp: 10}},
		Bindings: map[string]trafficCounter{
			bindingEmail("phone", "raw"): {TotalUp: 6},
			bindingEmail("phone", "cdn"): {TotalUp: 4},
		},
	}
	if err = saveTrafficStore(config, state); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireConfigLock(config, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = resetUserTrafficLocked(config, bin, "127.0.0.1:10085", "phone")
	releaseConfigLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	for _, alias := range []string{"phone", bindingEmail("phone", "raw"), bindingEmail("phone", "cdn")} {
		if !strings.Contains(text, "user>>>"+alias+">>>") {
			t.Fatalf("reset missed alias %q: %s", alias, text)
		}
	}
	after, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if after.Users["phone"].TotalUp != 0 {
		t.Fatal("owner reset failed")
	}
	for email := range after.Bindings {
		if bindingOwner(email) == "phone" && after.Bindings[email].TotalUp != 0 {
			t.Fatalf("binding %s not reset", email)
		}
	}
}

func TestPartialUserResetPreservesTotalsAndRebasesCompletedAliases(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	bin := filepath.Join(dir, "xray")
	mock := `#!/bin/sh
case "$*" in
  *"user>>>phone+Y2Ru>>>"*) echo 'failed' >&2; exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	root := testConfig(t)
	if _, err := applyManagedUserChange(root, UserChange{Kind: "add", User: "phone", Node: "all"}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	state := trafficStore{
		Users:    map[string]trafficCounter{"phone": {TotalUp: 200, LastUp: 100, Epoch: "old"}},
		Bindings: map[string]trafficCounter{bindingEmail("phone", "cdn"): {TotalUp: 20, LastUp: 20, Epoch: "old"}},
	}
	if err = saveTrafficStore(config, state); err != nil {
		t.Fatal(err)
	}
	if err = resetUserTraffic(config, bin, "127.0.0.1:10085", "phone"); err == nil {
		t.Fatal("partial failure was not reported")
	}
	after, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if after.Users["phone"].TotalUp != 200 || after.Users["phone"].LastUp != 0 {
		t.Fatalf("completed alias not rebased: %+v", after.Users["phone"])
	}
	if after.Bindings[bindingEmail("phone", "cdn")].LastUp != 20 {
		t.Fatal("failed alias watermark changed")
	}
}
