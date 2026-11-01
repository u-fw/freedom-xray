package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrafficCounterIdempotentAndRestartEpoch(t *testing.T) {
	old := trafficCounter{}
	first := updatedCounter(old, 100, 60, "epoch-a")
	if first.TotalUp != 100 || first.TotalDown != 60 {
		t.Fatalf("initial: %+v", first)
	}
	same := updatedCounter(first, 100, 60, "epoch-a")
	if same.TotalUp != 100 || same.TotalDown != 60 {
		t.Fatalf("double counted: %+v", same)
	}
	next := updatedCounter(same, 145, 90, "epoch-a")
	if next.TotalUp != 145 || next.TotalDown != 90 {
		t.Fatalf("delta wrong: %+v", next)
	}
	// Restarted core can accumulate MORE bytes than the previous epoch before
	// this collector wakes: a decrease heuristic alone would undercount.
	restarted := updatedCounter(next, 200, 110, "epoch-b")
	if restarted.TotalUp != 345 || restarted.TotalDown != 200 {
		t.Fatalf("epoch must add full new counters: %+v", restarted)
	}
	again := updatedCounter(restarted, 200, 110, "epoch-b")
	if again.TotalUp != 345 || again.TotalDown != 200 {
		t.Fatalf("epoch repeated: %+v", again)
	}
}

func TestTrafficSnapshotPreservesAbsentRows(t *testing.T) {
	state := trafficStore{Users: map[string]trafficCounter{}, Inbounds: map[string]trafficCounter{}}
	users := []UserTraffic{{Email: "alice", Uplink: 123, Downlink: 456}}
	ins := []InboundTraffic{{Tag: "node-a", Uplink: 50, Downlink: 70}, {Tag: "api-internal", Uplink: 900}}
	if !applyTrafficSnapshot(&state, users, ins, "test-epoch") {
		t.Fatal("expected state change")
	}
	if applyTrafficSnapshot(&state, users, ins, "test-epoch") {
		t.Fatal("duplicate refresh changed state")
	}
	if len(state.Inbounds) != 1 {
		t.Fatalf("internal or duplicate inbound recorded: %+v", state.Inbounds)
	}
	if applyTrafficSnapshot(&state, nil, nil, "test-epoch") {
		t.Fatal("empty sample should preserve old records")
	}
	if state.Users["alice"].TotalDown != 456 || state.Inbounds["node-a"].TotalUp != 50 {
		t.Fatalf("lost historical rows: %+v", state)
	}
}

func TestPersistentTrafficLegacyMigrationAndRename(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	raw := `{"users":{"alice":{"total_up":999,"total_down":111,"last_up":99,"last_down":11}}}`
	if err := os.WriteFile(trafficStorePath(config), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if state.Users["alice"].TotalUp != 999 {
		t.Fatalf("legacy total lost: %+v", state)
	}
	if err = renameTrafficStore(config, "alice", "bob"); err != nil {
		t.Fatal(err)
	}
	newer, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := newer.Users["alice"]; ok {
		t.Fatal("old user still present")
	}
	if newer.Users["bob"].TotalUp != 999 || newer.Users["bob"].LastUp != 0 {
		t.Fatalf("rename damaged history: %+v", newer)
	}
	if err = resetTrafficStore(config); err != nil {
		t.Fatal(err)
	}
	reset, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Users["bob"].TotalUp != 0 {
		t.Fatalf("reset failed: %+v", reset)
	}
	data, err := os.ReadFile(trafficStorePath(config))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err = json.Unmarshal(data, &parsed); err != nil {
		t.Fatal("invalid persisted JSON", err)
	}
}

func TestTrafficCorruptFileNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	path := trafficStorePath(config)
	corrupt := []byte("not valid JSON")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTrafficStore(config); err == nil {
		t.Fatal("silently recovered corrupt data")
	}
	if err := resetTrafficStore(config); err == nil {
		t.Fatal("reset overwrote corrupt state")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(corrupt) {
		t.Fatal("corrupt journal should be preserved for recovery")
	}
}

func TestTrafficCollectorEndToEnd(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"inbounds":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	epoch := filepath.Join(dir, "epoch.txt")
	userData := filepath.Join(dir, "users.json")
	inboundData := filepath.Join(dir, "inbounds.json")
	systemctl := filepath.Join(dir, "systemctl")
	xray := filepath.Join(dir, "xray")

	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\ncat '"+epoch+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cli := "#!/bin/sh\ncase \"$*\" in *inbound\\>\\>\\>*) cat '" + inboundData + "' ;; *) cat '" + userData + "' ;; esac\n"
	if err := os.WriteFile(xray, []byte(cli), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	set := func(e string, up, down uint64) {
		t.Helper()
		if err := os.WriteFile(epoch, []byte(strings.Repeat(e, 32)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		raw := fmt.Sprintf(`{"stat":[{"name":"user>>>alice>>>traffic>>>uplink","value":"%d"},{"name":"user>>>alice>>>traffic>>>downlink","value":"%d"}]}`, up, down)
		if err := os.WriteFile(userData, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		raw = fmt.Sprintf(`{"stat":[{"name":"inbound>>>node-one>>>traffic>>>uplink","value":"%d"},{"name":"inbound>>>node-one>>>traffic>>>downlink","value":"%d"}]}`, up, down)
		if err := os.WriteFile(inboundData, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Use shell quotes exactly as they will appear in generated shell scripts.
	set("a", 120, 60)
	first, err := snapshotTraffic(config, xray, "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	if first.Users["alice"].TotalUp != 120 {
		t.Fatalf("first snapshot: %#v", first)
	}
	again, err := snapshotTraffic(config, xray, "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	if again.Users["alice"].TotalUp != 120 {
		t.Fatal("double-counted same Xray runtime")
	}
	set("a", 170, 90)
	more, err := snapshotTraffic(config, xray, "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	if more.Inbounds["node-one"].TotalDown != 90 {
		t.Fatalf("missing node delta: %+v", more)
	}
	// New Xray incarnation already has higher counters than the old process.
	set("b", 400, 200)
	restarted, err := snapshotTraffic(config, xray, "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Users["alice"].TotalUp != 570 || restarted.Inbounds["node-one"].TotalDown != 290 {
		t.Fatalf("restart lost counters: %+v", restarted)
	}
}

func TestDeleteUserAccountingWithoutTouchingInboundTotals(t *testing.T) {
	state := trafficStore{
		Users:      map[string]trafficCounter{"alice": {TotalUp: 10}, "bob": {TotalUp: 20}},
		Inbounds:   map[string]trafficCounter{"reality": {TotalUp: 30}},
		LastOnline: map[string]int64{"alice": 123, "bob": 456},
		OnlineIPs:  map[string][]onlineClientIP{"alice": {{IP: "203.0.113.1", LastSeen: 123}}},
		LiveIPs:    map[string][]onlineClientIP{"alice": {{IP: "203.0.113.1", LastSeen: 123}}},
	}
	if !discardTrafficUser(&state, "alice") {
		t.Fatal("expected change")
	}
	if discardTrafficUser(&state, "alice") {
		t.Fatal("second purge must be idempotent")
	}
	if _, ok := state.Users["alice"]; ok {
		t.Fatal("deleted user retains traffic")
	}
	if _, ok := state.LastOnline["alice"]; ok {
		t.Fatal("deleted user retains online time")
	}
	if _, ok := state.OnlineIPs["alice"]; ok {
		t.Fatal("deleted user retains IP history")
	}
	if _, ok := state.LiveIPs["alice"]; ok {
		t.Fatal("deleted user retains live IP")
	}
	if state.Users["bob"].TotalUp != 20 || state.Inbounds["reality"].TotalUp != 30 {
		t.Fatal("unrelated statistics changed")
	}
}

// The Telegram per-user reset must reject a name that is not a valid managed
// user before it touches the lock or the ledger.
func TestResetUserTrafficRejectsUnsafeName(t *testing.T) {
	if err := resetUserTraffic(filepath.Join(t.TempDir(), "config.json"), "xray", "127.0.0.1:10085", "bad>>>name"); err == nil {
		t.Fatal("unsafe user name was accepted")
	}
}

func TestReconcileOrphanedTrafficAndPreserveActiveUsers(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	raw := `{"inbounds":[{"tag":"reality","protocol":"vless","settings":{"clients":[{"email":"alice","id":"99999999-9999-4999-8999-999999999999"}]}}]}`
	if err := os.WriteFile(config, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	state := trafficStore{Users: map[string]trafficCounter{
		"alice": {TotalUp: 12}, "removed": {TotalUp: 20},
	}, Inbounds: map[string]trafficCounter{"reality": {TotalUp: 32}}}
	changed, err := reconcileTrafficUsersLocked(config, &state)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected deletion of orphaned row")
	}
	if _, exists := state.Users["removed"]; exists {
		t.Fatal("deleted user remains")
	}
	if state.Users["alice"].TotalUp != 12 || state.Inbounds["reality"].TotalUp != 32 {
		t.Fatal("valid statistics removed")
	}
	if changed, err = reconcileTrafficUsersLocked(config, &state); err != nil || changed {
		t.Fatalf("unexpected second reconciliation: %v %v", changed, err)
	}
}
