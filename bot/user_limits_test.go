package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseQuotaGB(t *testing.T) {
	for _, test := range []struct {
		input string
		want  int64
		clear bool
	}{
		{"100", 100 * gibBytes, false},
		{"0.5", gibBytes / 2, false},
		{"0", 0, true},
		{"-", 0, true},
	} {
		bytes, clear, err := parseQuotaGB(test.input)
		if err != nil {
			t.Fatalf("rejected %q: %v", test.input, err)
		}
		if bytes != test.want || clear != test.clear {
			t.Fatalf("%q parsed as %d clear=%v", test.input, bytes, clear)
		}
	}
	for _, input := range []string{"", "abc", "-1", "NaN", "Inf", ".", "1048577"} {
		if _, _, err := parseQuotaGB(input); err == nil {
			t.Fatalf("accepted invalid quota %q", input)
		}
	}
	if bytes, _, err := parseQuotaGB("1"); err != nil || bytes != gibBytes {
		t.Fatalf("1 GB must be one GiB: %d %v", bytes, err)
	}
}

func TestExpiryEnforcedWhenTrafficLedgerUnavailable(t *testing.T) {
	root := testConfig(t)
	for _, name := range []string{"expired-user", "quota-user"} {
		if _, err := applyManagedUserChange(root, UserChange{Kind: "add", User: name, Node: "raw"}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	h := newBindingMigrationHarness(t, string(raw), false)
	if err = os.WriteFile(trafficStorePath(h.config), []byte("invalid ledger"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = updateUserLimits(h.config, func(users map[string]userLimit) error {
		users["expired-user"] = userLimit{ExpiryUnix: time.Now().Add(-time.Hour).Unix()}
		users["quota-user"] = userLimit{QuotaBytes: 1, Notified80: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	cfg.XrayBinPath = h.xray
	cfg.AllowMutations = true
	(&BotService{cfg: cfg}).enforceUserLimitsOnce()
	suspended, _, err := readSuspendedStore(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := suspended.Users["expired-user"]; !ok {
		t.Fatal("expired user remained active when the ledger failed")
	}
	if _, ok := suspended.Users["quota-user"]; ok {
		t.Fatal("quota was enforced without traffic data")
	}
	limits, err := snapshotUserLimits(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if !limits["quota-user"].Notified80 {
		t.Fatal("unavailable traffic cleared quota notification state")
	}
}

func TestLocalUserChangesMoveAndRemoveLimits(t *testing.T) {
	h := newBindingMigrationHarness(t, bindingMigrationConfig(), false)
	if err := updateUserLimits(h.config, func(users map[string]userLimit) error {
		users["alice"] = userLimit{QuotaBytes: 123, ExpiryUnix: 1800000000}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "rename", User: "alice", NewName: "bob"}); err != nil {
		t.Fatal(err)
	}
	limits, err := snapshotUserLimits(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := limits["alice"]; ok {
		t.Fatal("old user retained limits")
	}
	if limits["bob"].QuotaBytes != 123 || limits["bob"].ExpiryUnix != 1800000000 {
		t.Fatalf("limits did not follow local rename: %+v", limits)
	}
	if _, err = changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "delete", User: "bob"}); err != nil {
		t.Fatal(err)
	}
	limits, err = snapshotUserLimits(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := limits["bob"]; ok {
		t.Fatal("deleted user retained limits")
	}
}

func TestRenameRejectsSuspendedUserName(t *testing.T) {
	h := newBindingMigrationHarness(t, bindingMigrationConfig(), false)
	parked := `{"users":{"bob":{"uuid":"parked","nodes":{"raw":{"id":"parked","email":"bob"}}}}}`
	if err := os.WriteFile(suspendedStorePath(h.config), []byte(parked), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := changeManagedUser(h.config, h.xray, "127.0.0.1:10085", UserChange{Kind: "rename", User: "alice", NewName: "bob"}); err == nil {
		t.Fatal("rename reused a suspended account's name")
	}
	if h.restartCount(t) != 0 {
		t.Fatal("rejected rename restarted Xray")
	}
}

func TestLimitRecheckHonorsRaisedQuotaAndReset(t *testing.T) {
	root := testConfig(t)
	if _, err := applyManagedUserChange(root, UserChange{Kind: "add", User: "phone", Node: "raw"}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	h := newBindingMigrationHarness(t, string(raw), false)
	state := trafficStore{Users: map[string]trafficCounter{"phone": {TotalUp: 200}}}
	if err = saveTrafficStore(h.config, state); err != nil {
		t.Fatal(err)
	}
	if err = updateUserLimits(h.config, func(users map[string]userLimit) error { users["phone"] = userLimit{QuotaBytes: 300}; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, suspended, err := recheckAndSuspendLimitedUser(h.config, h.xray, "127.0.0.1:10085", "phone", time.Now()); err != nil || suspended {
		t.Fatalf("raised quota ignored: suspended=%v err=%v", suspended, err)
	}
	if err = updateUserLimits(h.config, func(users map[string]userLimit) error { users["phone"] = userLimit{QuotaBytes: 100}; return nil }); err != nil {
		t.Fatal(err)
	}
	state.Users["phone"] = trafficCounter{}
	if err = saveTrafficStore(h.config, state); err != nil {
		t.Fatal(err)
	}
	if _, _, suspended, err := recheckAndSuspendLimitedUser(h.config, h.xray, "127.0.0.1:10085", "phone", time.Now()); err != nil || suspended {
		t.Fatalf("traffic reset ignored: suspended=%v err=%v", suspended, err)
	}
	if h.restartCount(t) != 0 {
		t.Fatal("stale limit decision restarted Xray")
	}
}

func TestParseExpiryInput(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	stamp, clear, err := parseExpiryInput("30d", now)
	if err != nil || clear {
		t.Fatalf("30d rejected: %v clear=%v", err, clear)
	}
	if want := now.AddDate(0, 0, 30).Unix(); stamp != want {
		t.Fatalf("30d = %d, want %d", stamp, want)
	}
	stamp, clear, err = parseExpiryInput("2026-02-01", now)
	if err != nil || clear {
		t.Fatalf("date rejected: %v clear=%v", err, clear)
	}
	if want := time.Date(2026, 2, 1, 23, 59, 59, 0, time.UTC).Unix(); stamp != want {
		t.Fatalf("date end = %d, want %d", stamp, want)
	}
	for _, input := range []string{"0", "-"} {
		if _, clear, err := parseExpiryInput(input, now); err != nil || !clear {
			t.Fatalf("%q must clear: %v", input, err)
		}
	}
	for _, input := range []string{"", "abc", "2025-12-31", "2026-13-01", "-5d", "36501d"} {
		if _, _, err := parseExpiryInput(input, now); err == nil {
			t.Fatalf("accepted invalid expiry %q", input)
		}
	}
}

func TestEvaluateUserLimit(t *testing.T) {
	now := time.Unix(1800000000, 0)
	quota := userLimit{QuotaBytes: 1000}
	if action := evaluateUserLimit(quota, 799, now); action.Warn80 || action.Disable {
		t.Fatalf("warned below threshold: %+v", action)
	}
	if action := evaluateUserLimit(quota, 800, now); !action.Warn80 || action.Disable {
		t.Fatalf("80%% threshold wrong: %+v", action)
	}
	quota.Notified80 = true
	if action := evaluateUserLimit(quota, 800, now); action.Warn80 {
		t.Fatalf("80%% warning repeated: %+v", action)
	}
	if action := evaluateUserLimit(quota, 1000, now); !action.Disable || action.Reason != "quota" || !action.WarnQuota {
		t.Fatalf("quota not enforced: %+v", action)
	}
	quota.NotifiedQuota = true
	if action := evaluateUserLimit(quota, 1000, now); !action.Disable || action.WarnQuota {
		t.Fatalf("quota notification repeated: %+v", action)
	}

	expiry := userLimit{ExpiryUnix: now.Unix() + 3*24*3600 - 1}
	if action := evaluateUserLimit(expiry, 0, now); !action.WarnExpiry || action.Disable {
		t.Fatalf("3-day warning missing: %+v", action)
	}
	farther := userLimit{ExpiryUnix: now.Unix() + 3*24*3600 + 1}
	if action := evaluateUserLimit(farther, 0, now); action.WarnExpiry || action.Disable {
		t.Fatalf("premature expiry warning: %+v", action)
	}
	expired := userLimit{ExpiryUnix: now.Unix()}
	action := evaluateUserLimit(expired, 0, now)
	if !action.Disable || action.Reason != "expired" || !action.WarnExpired {
		t.Fatalf("expiry not enforced: %+v", action)
	}
	expired.NotifiedExpired = true
	if action := evaluateUserLimit(expired, 0, now); !action.Disable || action.WarnExpired {
		t.Fatalf("expiry notification repeated: %+v", action)
	}
}

func TestUserLimitStoreRoundTrip(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	email := "phone"
	err := updateUserLimits(config, func(users map[string]userLimit) error {
		users[email] = userLimit{QuotaBytes: 64 * gibBytes, ExpiryUnix: 1800000000}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	limits, err := snapshotUserLimits(config)
	if err != nil {
		t.Fatal(err)
	}
	limit, ok := limits[email]
	if !ok || limit.QuotaBytes != 64*gibBytes || limit.ExpiryUnix != 1800000000 {
		t.Fatalf("limit not persisted: %+v", limits)
	}
	markUserLimit(config, email, func(l *userLimit) { l.Notified80 = true })
	limits, _ = snapshotUserLimits(config)
	if !limits[email].Notified80 {
		t.Fatal("notification flag not persisted")
	}
	if err := updateUserLimits(config, func(users map[string]userLimit) error { delete(users, email); return nil }); err != nil {
		t.Fatal(err)
	}
	limits, _ = snapshotUserLimits(config)
	if len(limits) != 0 {
		t.Fatalf("limit not removed: %+v", limits)
	}
	if err := os.WriteFile(userLimitStorePath(config), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotUserLimits(config); err == nil {
		t.Fatal("accepted corrupted limit store")
	}
}
