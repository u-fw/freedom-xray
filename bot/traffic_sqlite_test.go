package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSQLiteMigrationBackupAndRestartPersistence(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	legacy := []byte(`{"users":{"alice":{"total_up":900,"total_down":100,"last_up":90,"last_down":10,"epoch":"abc"}},"inbounds":{"node":{"total_up":800,"total_down":99,"last_up":80,"last_down":9,"epoch":"abc"}}}`)
	if err := os.WriteFile(trafficStorePath(config), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if state.Users["alice"].TotalUp != 900 || state.Inbounds["node"].TotalDown != 99 {
		t.Fatalf("lost legacy counters: %+v", state)
	}
	bak, err := os.ReadFile(trafficStorePath(config) + ".pre-sqlite.bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(bak) != string(legacy) {
		t.Fatal("legacy backup changed")
	}
	if err = saveTrafficStore(config, state); err != nil {
		t.Fatal(err)
	}
	restored, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Users["alice"].TotalUp != 900 || restored.Inbounds["node"].TotalDown != 99 {
		t.Fatalf("failed to persist after restart: %+v", restored)
	}
	bak, err = os.ReadFile(trafficStorePath(config) + ".pre-sqlite.bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(bak) != string(legacy) {
		t.Fatal("immutable backup overwritten")
	}
}

func TestTrafficDatabaseAndWALStayPrivate(t *testing.T) {
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)
	config := filepath.Join(t.TempDir(), "config.json")
	db, err := openTrafficDB(config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(trafficDBPath(config) + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions: %o", suffix, info.Mode().Perm())
		}
		if err = os.Chmod(trafficDBPath(config)+suffix, 0644); err != nil {
			t.Fatal(err)
		}
	}
	second, err := openTrafficDB(config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(trafficDBPath(config) + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("existing %s permissions were not repaired: %o", suffix, info.Mode().Perm())
		}
	}
}

func TestTrafficSaveOnlyWritesChangedRows(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	state := trafficStore{
		Users:        map[string]trafficCounter{"alice": {TotalUp: 10}, "bob": {TotalDown: 20}},
		OnlineIPs:    map[string][]onlineClientIP{"alice": {{IP: "203.0.113.1", LastSeen: 100}}},
		OnlinePollAt: 100,
	}
	if err := saveTrafficStore(config, state); err != nil {
		t.Fatal(err)
	}
	db, err := openTrafficDB(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"CREATE TABLE audit (kind TEXT NOT NULL)",
		"CREATE TRIGGER audit_insert AFTER INSERT ON counters BEGIN INSERT INTO audit VALUES ('insert'); END",
		"CREATE TRIGGER audit_update AFTER UPDATE ON counters BEGIN INSERT INTO audit VALUES ('update'); END",
		"CREATE TRIGGER audit_delete AFTER DELETE ON counters BEGIN INSERT INTO audit VALUES ('delete'); END",
		"CREATE TRIGGER audit_ip_delete AFTER DELETE ON online_ips BEGIN INSERT INTO audit VALUES ('ip-delete'); END",
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	state.OnlinePollAt = 105
	if err = saveTrafficStore(config, state); err != nil {
		t.Fatal(err)
	}
	db, err = openTrafficDB(config)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM audit").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unchanged rows were rewritten %d times", count)
	}
	db.Close()
	state.Users["alice"] = trafficCounter{TotalUp: 15}
	delete(state.Users, "bob")
	if err = saveTrafficStore(config, state); err != nil {
		t.Fatal(err)
	}
	db, err = openTrafficDB(config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.QueryRow("SELECT count(*) FROM audit").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected one update and one delete, got %d writes", count)
	}
	saved, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Users["alice"].TotalUp != 15 || len(saved.Users) != 1 || len(saved.OnlineIPs["alice"]) != 1 {
		t.Fatalf("changed state was not persisted: %+v", saved)
	}
}

func TestSQLiteOnlineObservationsAndRetention(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	state, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	if !applyOnlineSnapshot(&state, []onlineUser{{Email: "alice", IPs: []onlineClientIP{{IP: "203.0.113.4", LastSeen: now.Unix()}}}}, now) {
		t.Fatal("online snapshot unchanged")
	}
	if err = saveTrafficStore(config, state); err != nil {
		t.Fatal(err)
	}
	again, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	ips, known := recentOnline(again, "alice", now)
	if !known || len(ips) != 1 || ips[0].IP != "203.0.113.4" {
		t.Fatalf("live state missing: %+v %v", ips, known)
	}
	if !applyOnlineSnapshot(&again, nil, now.Add(5*time.Second)) {
		t.Fatal("offline transition not recorded")
	}
	if len(again.LiveIPs) != 0 {
		t.Fatal("stale live IP retained")
	}
	if again.LastOnline["alice"] != now.Unix() {
		t.Fatal("offline poll changed last seen")
	}
	if !applyOnlineSnapshot(&again, nil, now.Add(31*time.Minute)) {
		t.Fatal("stale IP not pruned")
	}
	if len(again.OnlineIPs) != 1 {
		t.Fatal("history inside the retention window was pruned")
	}
	if !applyOnlineSnapshot(&again, nil, now.Add(onlineHistoryRetention+time.Minute)) {
		t.Fatal("expired IP not pruned")
	}
	if len(again.OnlineIPs) != 0 {
		t.Fatal("expired IP history retained")
	}
	if _, known = recentOnline(again, "alice", now.Add(onlineHistoryRetention+2*time.Minute)); known {
		t.Fatal("stale poll must not report online")
	}
}

func TestSQLiteCorruptLegacyFailsClosed(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	original := []byte("{invalid")
	if err := os.WriteFile(trafficStorePath(config), original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTrafficStore(config); err == nil {
		t.Fatal("corrupt legacy journal was accepted")
	}
	data, err := os.ReadFile(trafficStorePath(config))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatal("corrupt file was replaced")
	}
}

func TestSQLiteInvalidPollTimestampFailsClosed(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	db, err := openTrafficDB(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE meta SET value='invalid' WHERE key='online_poll_at'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = loadTrafficStore(config); err == nil {
		t.Fatal("invalid metadata was silently accepted")
	}
}

func TestOnlinePollTimestampAndIPValidation(t *testing.T) {
	now := time.Now().Unix()
	raw, _ := json.Marshal(now)
	if parseOnlineTimestamp(raw, now) != now {
		t.Fatal("numeric timestamp was not parsed")
	}
	if parseOnlineTimestamp([]byte(`"1"`), now) != 1 {
		t.Fatal("string timestamp was not parsed")
	}
	if parseOnlineTimestamp([]byte(`"bogus"`), now) != now {
		t.Fatal("invalid timestamp should fall back")
	}
}
