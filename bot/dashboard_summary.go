package main

import (
	"fmt"
	"time"
)

// dashboardSummary condenses locally stored presence, traffic and limits for
// the dashboard. It reads no network service, so the dashboard stays fast.
func dashboardSummary(lang, configPath string, now time.Time) htmlText {
	state, err := loadTrafficStore(configPath)
	if err != nil {
		return ""
	}
	limits, _ := snapshotUserLimits(configPath)
	return summarizeDashboard(lang, state, limits, now)
}

func summarizeDashboard(lang string, state trafficStore, limits map[string]userLimit, now time.Time) htmlText {
	var total uint64
	for _, counter := range state.Users {
		total += counter.TotalUp + counter.TotalDown
	}
	// A stale poll says nothing about who is connected now.
	online := "—"
	if _, fresh := recentOnline(state, "", now); fresh {
		count := 0
		for _, ips := range state.LiveIPs {
			if len(ips) > 0 {
				count++
			}
		}
		online = fmt.Sprint(count)
	}
	summary := trh(lang, "menu.summary", online, FormatBytes(total))
	nearQuota, expiring := 0, 0
	for email, limit := range limits {
		if limit.QuotaBytes > 0 {
			counter := state.Users[email]
			if (counter.TotalUp+counter.TotalDown)*100 >= uint64(limit.QuotaBytes)*80 {
				nearQuota++
			}
		}
		if limit.ExpiryUnix > 0 && limit.ExpiryUnix-now.Unix() <= int64(3*24*time.Hour/time.Second) {
			expiring++
		}
	}
	if nearQuota+expiring > 0 {
		summary += trh(lang, "menu.summary.alerts", nearQuota, expiring)
	}
	return summary
}
