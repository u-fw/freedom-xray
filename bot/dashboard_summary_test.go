package main

import (
	"strings"
	"testing"
	"time"
)

func TestDashboardSummaryReportsPresenceTrafficAndAlerts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	state := trafficStore{
		Users: map[string]trafficCounter{
			"alice": {TotalUp: 850, TotalDown: 0},
			"bob":   {TotalUp: 100, TotalDown: 50},
		},
		LiveIPs: map[string][]onlineClientIP{
			"alice": {{IP: "203.0.113.7"}},
			"bob":   {{IP: "203.0.113.8"}},
			"carol": nil,
		},
		OnlinePollAt: now.Unix() - 5,
	}
	limits := map[string]userLimit{
		"alice": {QuotaBytes: 1000},
		"bob":   {QuotaBytes: 1000, ExpiryUnix: now.Add(48 * time.Hour).Unix()},
		"dave":  {ExpiryUnix: now.Add(30 * 24 * time.Hour).Unix()},
	}
	summary := plainHTML(summarizeDashboard("en", state, limits, now))
	if !strings.Contains(summary, "Online now: 2") || !strings.Contains(summary, "User traffic: "+FormatBytes(1000)) {
		t.Fatalf("presence or traffic missing: %q", summary)
	}
	if !strings.Contains(summary, "Near quota: 1 | Expiring within 3 days: 1") {
		t.Fatalf("alerts = %q", summary)
	}

	state.OnlinePollAt = now.Unix() - 600
	summary = plainHTML(summarizeDashboard("en", state, map[string]userLimit{"dave": limits["dave"]}, now))
	if !strings.Contains(summary, "Online now: —") || strings.Contains(summary, "Near quota") {
		t.Fatalf("stale presence or quiet alerts = %q", summary)
	}
}
