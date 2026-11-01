package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestREALITYOnlineAPIParsesSourceIP(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	mock := `#!/bin/sh
printf '%s\n' '{"users":[{"email":"phone","ips":[{"ip":"203.0.113.7","lastSeen":"1800000000"},{"ip":"not-an-ip","lastSeen":"1800000000"}]},{"email":"alice","ips":[{"ip":"2001:db8::5","lastSeen":"1800000000"}]}]}'
`
	if err := os.WriteFile(bin, []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	users, err := queryOnlineClients(ctx, bin, "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("unexpected users: %+v", users)
	}
	if len(users[0].IPs) != 1 || users[0].IPs[0].IP != "203.0.113.7" {
		t.Fatalf("REALITY IPv4 source missing: %+v", users)
	}
	if len(users[1].IPs) != 1 || users[1].IPs[0].IP != "2001:db8::5" {
		t.Fatalf("IPv6 source missing: %+v", users)
	}
}

func TestPartialOnlineFallbackDoesNotMarkOthersOffline(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	mock := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    -all) printf '%s\n' '{"users":[]}'; exit 0 ;;
    -email=alice) printf '%s\n' '{"name":"alice","ips":{"203.0.113.7":1800000000}}'; exit 0 ;;
    -email=bob) echo 'temporary failure' >&2; exit 1 ;;
  esac
done
exit 1
`
	if err := os.WriteFile(bin, []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	partial, err := queryOnlineClientsByEmail(ctx, bin, "127.0.0.1:10085", []string{"alice", "bob"})
	if err == nil || len(partial) != 1 {
		t.Fatalf("expected partial results and an error: %+v %v", partial, err)
	}
	for i := 0; i < 2; i++ {
		if _, err = queryOnlineClientsForEmails(ctx, bin, "127.0.0.1:10085", []string{"alice", "bob"}); err == nil {
			t.Fatal("incomplete fallback was accepted as a complete offline snapshot")
		}
	}
}

func TestMalformedAggregateOnlineEntryRejectsWholeSnapshot(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	mock := `#!/bin/sh
printf '%s\n' '{"users":[{"email":"alice","ips":[{"ip":"203.0.113.7","lastSeen":1800000000}]},{"email":"bob","ips":42}]}'
`
	if err := os.WriteFile(bin, []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := queryOnlineClients(ctx, bin, "127.0.0.1:10085"); err == nil {
		t.Fatal("partial aggregate snapshot was accepted")
	}
}

func TestOnlineCardShowsSourceAndCumulativeUserTraffic(t *testing.T) {
	now := time.Now()
	state := trafficStore{
		Users:        map[string]trafficCounter{"phone": {TotalUp: 1048576, TotalDown: 2097152}},
		LiveIPs:      map[string][]onlineClientIP{"phone": {{IP: "203.0.113.7", LastSeen: now.Unix()}}},
		OnlineIPs:    map[string][]onlineClientIP{"phone": {{IP: "203.0.113.7", LastSeen: now.Unix()}}},
		LastOnline:   map[string]int64{"phone": now.Unix()},
		OnlinePollAt: now.Unix(),
	}
	body := onlineCardText(state, now, "en")
	for _, wanted := range []string{"phone", "203.0.113.7", "Online", "Total:", "cumulative total", "Last online", "IP history"} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("missing %q in online view: %s", wanted, body)
		}
	}
	state.LiveIPs = map[string][]onlineClientIP{}
	body = onlineCardText(state, now, "en")
	if !strings.Contains(body, "Recently observed (not online)") {
		t.Fatalf("recent IP was lost or mislabeled: %s", body)
	}
}

func TestOnlineCardKeepsUserWithoutObservedIP(t *testing.T) {
	state := trafficStore{Users: map[string]trafficCounter{"reality-user": {TotalDown: 1024}}, OnlinePollAt: time.Now().Unix()}
	message := onlineCardText(state, time.Now(), "en")
	if !strings.Contains(message, "reality-user") || !strings.Contains(message, "Source IP not observed") || !strings.Contains(message, "Total:") {
		t.Fatalf("user with traffic must remain visible without a captured IP: %s", message)
	}
}

// The XFF spoofing caveat only matters when addresses are actually shown.
func TestOnlineCardShowsXFFWarningOnlyWithDisplayedIPs(t *testing.T) {
	now := time.Now()
	state := trafficStore{Users: map[string]trafficCounter{"u": {TotalUp: 1}}, OnlinePollAt: now.Unix()}
	if strings.Contains(onlineCardText(state, now, "en"), "XFF") {
		t.Fatal("spoofing warning shown without any displayed IP")
	}
	state.OnlineIPs = map[string][]onlineClientIP{"u": {{IP: "203.0.113.7", LastSeen: now.Unix()}}}
	if !strings.Contains(onlineCardText(state, now, "en"), "XFF") {
		t.Fatal("spoofing warning missing while an IP is displayed")
	}
}

// REALITY sessions often close between two polls. A counter increase must
// still be recorded as last-online activity, and the card must say so without
// claiming a source IP.
func TestTrafficIncreaseMarksActivityWithoutOnlineIP(t *testing.T) {
	now := time.Now()
	state := trafficStore{
		Users:      map[string]trafficCounter{"reality-user": {TotalUp: 10, TotalDown: 20}},
		LastOnline: map[string]int64{}, LastActive: map[string]int64{},
		OnlineIPs: map[string][]onlineClientIP{}, LiveIPs: map[string][]onlineClientIP{},
	}
	previous := map[string]trafficCounter{"reality-user": {TotalUp: 10, TotalDown: 20}}
	if inferOnlineFromTraffic(&state, previous, now) {
		t.Fatal("unchanged traffic must not mark activity")
	}
	if state.LastOnline["reality-user"] != 0 {
		t.Fatalf("unexpected last-online: %+v", state.LastOnline)
	}
	state.Users["reality-user"] = trafficCounter{TotalUp: 10, TotalDown: 4096}
	if !inferOnlineFromTraffic(&state, previous, now) {
		t.Fatal("traffic increase must be persisted")
	}
	if state.LastOnline["reality-user"] != now.Unix() {
		t.Fatalf("last-online not updated: %+v", state.LastOnline)
	}
	if state.LastActive["reality-user"] != now.Unix() {
		t.Fatalf("last-active not updated: %+v", state.LastActive)
	}
	body := onlineCardText(state, now, "en")
	if !strings.Contains(body, "Active traffic") || !strings.Contains(body, "Last online") {
		t.Fatalf("activity status missing: %s", body)
	}
	// The activity window is bounded: stale activity falls back to the normal label.
	if !strings.Contains(onlineCardText(state, now.Add(2*onlineActivityWindow), "en"), "Source IP not observed") {
		t.Fatalf("stale activity must not claim live state: %s", onlineCardText(state, now.Add(2*onlineActivityWindow), "en"))
	}
}

// Older Xray-core releases implement statsonlineiplist for one email but do
// not support the aggregate -all flag. The Bot must keep collecting online IPs
// through the per-email compatibility path instead of logging an unsupported
// API on every sample.
func TestOnlinePerEmailFallbackForOlderCore(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	mock := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    -all) printf '%s\n' 'flag provided but not defined: -all' >&2; exit 2 ;;
    -email=*)
      email="${arg#-email=}"
      printf '{"name":"%s","ips":{"203.0.113.7":1800000000}}\n' "$email"
      exit 0 ;;
  esac
done
printf '%s\n' 'unexpected arguments' >&2
exit 3
`
	if err := os.WriteFile(bin, []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	users, err := queryOnlineClientsForEmails(ctx, bin, "127.0.0.1:10085", []string{"phone", "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("per-email fallback lost users: %+v", users)
	}
	seen := map[string]string{}
	for _, user := range users {
		if len(user.IPs) != 1 {
			continue
		}
		seen[user.Email] = user.IPs[0].IP
	}
	if seen["phone"] != "203.0.113.7" || seen["alice"] != "203.0.113.7" {
		t.Fatalf("per-email fallback addresses wrong: %+v", users)
	}
}

// Some core builds accept "-all" but return an empty list while the per-email
// query has data. The Bot probes once, then prefers per-email for a bounded
// period and backs off probing after an empty probe.
func TestOnlineEmptyAggregateFallsBackToPerEmail(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	mock := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    -all) printf '%s\n' '{"users":[]}'; exit 0 ;;
    -email=*)
      email="${arg#-email=}"
      printf '{"name":"%s","ips":{"203.0.113.9":1800000000}}\n' "$email"
      exit 0 ;;
  esac
done
exit 3
`
	if err := os.WriteFile(bin, []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	users, err := queryOnlineClientsForEmails(ctx, bin, "127.0.0.1:10085", []string{"phone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || len(users[0].IPs) != 1 || users[0].IPs[0].IP != "203.0.113.9" {
		t.Fatalf("empty aggregate did not fall back: %+v", users)
	}
	usePerEmail, allowProbe := onlineFallbackPlan(bin, 1, time.Now())
	if !usePerEmail || allowProbe {
		t.Fatalf("fallback not cached: use=%v probe=%v", usePerEmail, allowProbe)
	}
	users, err = queryOnlineClientsForEmails(ctx, bin, "127.0.0.1:10085", []string{"phone"})
	if err != nil || len(users) != 1 {
		t.Fatalf("cached per-email path failed: %+v %v", users, err)
	}
	usePerEmail, allowProbe = onlineFallbackPlan(bin, 1, time.Now().Add(11*time.Minute))
	if usePerEmail || !allowProbe {
		t.Fatalf("fallback did not expire: use=%v probe=%v", usePerEmail, allowProbe)
	}
	recordOnlineFallback(bin, false, time.Now())
	usePerEmail, allowProbe = onlineFallbackPlan(bin, 1, time.Now().Add(time.Minute))
	if usePerEmail || allowProbe {
		t.Fatalf("empty probe did not back off: use=%v probe=%v", usePerEmail, allowProbe)
	}
	if usePerEmail, allowProbe = onlineFallbackPlan(bin, 200, time.Now()); usePerEmail || allowProbe {
		t.Fatalf("oversized email list must not trigger a probe: use=%v probe=%v", usePerEmail, allowProbe)
	}
}

func TestOnlineSnapshotAggregatesBindingEmailsByOwner(t *testing.T) {
	now := time.Now()
	state := trafficStore{}
	ensureTrafficMaps(&state)
	raw := bindingEmail("alice", "raw")
	cdn := bindingEmail("alice", "cdn")
	observed := []onlineUser{
		{Email: raw, IPs: []onlineClientIP{{IP: "198.51.100.7", LastSeen: now.Unix() - 10}}},
		{Email: cdn, IPs: []onlineClientIP{{IP: "198.51.100.7", LastSeen: now.Unix() - 5}, {IP: "2001:db8::9", LastSeen: now.Unix() - 7}}},
		{Email: "bob", IPs: []onlineClientIP{{IP: "203.0.113.9", LastSeen: now.Unix() - 8}}},
	}
	if !applyOnlineSnapshot(&state, observed, now) {
		t.Fatal("snapshot did not change state")
	}
	if _, ok := state.LiveIPs[raw]; ok {
		t.Fatal("binding email leaked into live user entries")
	}
	if _, ok := state.LiveIPs[cdn]; ok {
		t.Fatal("binding email leaked into live user entries")
	}
	live := state.LiveIPs["alice"]
	if len(live) != 2 || live[0].IP != "198.51.100.7" || live[0].LastSeen != now.Unix()-5 {
		t.Fatalf("owner IP aggregation wrong: %+v", live)
	}
	if state.LastOnline["alice"] != now.Unix() {
		t.Fatalf("owner last-online missing: %+v", state.LastOnline)
	}
	if len(state.OnlineIPs["alice"]) != 2 {
		t.Fatalf("owner history not merged: %+v", state.OnlineIPs["alice"])
	}
	if len(state.OnlineIPs["bob"]) != 1 {
		t.Fatalf("legacy user history missing: %+v", state.OnlineIPs["bob"])
	}
}

func TestOnlineEmailResponseShapes(t *testing.T) {
	mapForm := []byte(`{"name":"phone","ips":{"203.0.113.7":1800000000}}`)
	user, err := parseOnlineEmailResponse(mapForm, "")
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "phone" || len(user.IPs) != 1 || user.IPs[0].IP != "203.0.113.7" {
		t.Fatalf("map form not parsed: %+v", user)
	}
	arrayForm := []byte(`{"email":"alice","ips":[{"ip":"2001:db8::5","lastSeen":"1800000000"}]}`)
	user, err = parseOnlineEmailResponse(arrayForm, "")
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "alice" || len(user.IPs) != 1 || user.IPs[0].IP != "2001:db8::5" {
		t.Fatalf("array form not parsed: %+v", user)
	}
}

func TestParseOnlineIPsShapes(t *testing.T) {
	ips, err := parseOnlineIPs([]byte(`[{"ip":"203.0.113.7","lastSeen":"1800000000"}]`), 1800000000)
	if err != nil || len(ips) != 1 || ips[0].IP != "203.0.113.7" {
		t.Fatalf("array shape not parsed: %+v %v", ips, err)
	}
	ips, err = parseOnlineIPs([]byte(`{"2001:db8::5":1800000000}`), 1800000000)
	if err != nil || len(ips) != 1 || ips[0].IP != "2001:db8::5" {
		t.Fatalf("map shape not parsed: %+v %v", ips, err)
	}
	if ips, err = parseOnlineIPs([]byte(`null`), 1800000000); err != nil || len(ips) != 0 {
		t.Fatalf("null must be empty: %+v %v", ips, err)
	}
	if _, err = parseOnlineIPs([]byte(`"bogus"`), 1800000000); err == nil {
		t.Fatal("accepted an unsupported ips shape")
	}
}

func TestParseStatInt(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int64
		ok   bool
	}{
		{`"3"`, 3, true},
		{`3`, 3, true},
		{`"0"`, 0, true},
		{`"bogus"`, 0, false},
		{``, 0, false},
	} {
		value, ok := parseStatInt([]byte(test.raw))
		if ok != test.ok || value != test.want {
			t.Fatalf("%q parsed as %d/%v", test.raw, value, ok)
		}
	}
}

// A malformed aggregate response or a failing aggregate RPC must not hide the
// working per-email endpoint: older cores return an unimplemented RPC for
// GetUsersStats while GetStatsOnlineIpList keeps working.
func TestOnlineAggregateFailureFallsBackToPerEmail(t *testing.T) {
	for _, mode := range []string{"rpc-error", "malformed"} {
		bin := filepath.Join(t.TempDir(), "xray")
		mock := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    -all)
      if [ "` + mode + `" = "malformed" ]; then
        printf '%s\n' '{"users":[{"email":"phone","ips":"bogus"}]}'
        exit 0
      fi
      printf '%s\n' 'failed to get stats: rpc error: code = Unimplemented desc = unknown service method' >&2
      exit 1 ;;
    -email=*)
      printf '%s\n' '{"name":"phone","ips":{"203.0.113.11":1800000000}}'
      exit 0 ;;
  esac
done
exit 3
`
		if err := os.WriteFile(bin, []byte(mock), 0700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		users, err := queryOnlineClientsForEmails(ctx, bin, "127.0.0.1:10085", []string{"phone"})
		cancel()
		if err != nil {
			t.Fatalf("%s: aggregate failure was not recovered: %v", mode, err)
		}
		if len(users) != 1 || len(users[0].IPs) != 1 || users[0].IPs[0].IP != "203.0.113.11" {
			t.Fatalf("%s: per-email fallback lost the user: %+v", mode, users)
		}
	}
}
