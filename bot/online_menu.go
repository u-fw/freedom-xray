package main

import (
	"sort"
	"strings"
	"time"
)

func onlineCardText(state trafficStore, now time.Time, lang string) htmlText {
	return onlineCardTextWithError(state, now, lang, nil)
}

func onlineCardTextWithError(state trafficStore, now time.Time, lang string, queryErr error) htmlText {
	var out htmlBuilder
	out.Add(trh(lang, "online.title"))
	out.Add(trh(lang, "online.history", int(onlineHistoryRetention/(24*time.Hour)), maxOnlineIPsPerUser))
	if queryErr != nil {
		out.Add(trh(lang, "online.queryFailure", queryErr.Error()))
	}
	fresh := state.OnlinePollAt > 0 && now.Unix()-state.OnlinePollAt <= 20 && state.OnlinePollAt <= now.Unix()+5
	if !fresh {
		out.Add(trh(lang, "online.stale"))
	}
	nameSet := map[string]bool{}
	for name, ips := range state.LiveIPs {
		if len(ips) > 0 {
			nameSet[name] = true
		}
	}
	for name, ips := range state.OnlineIPs {
		if len(ips) > 0 {
			nameSet[name] = true
		}
	}
	for name := range state.Users {
		nameSet[name] = true
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		iLive := fresh && len(state.LiveIPs[names[i]]) > 0
		jLive := fresh && len(state.LiveIPs[names[j]]) > 0
		if iLive != jLive {
			return iLive
		}
		iAt := state.LastOnline[names[i]]
		jAt := state.LastOnline[names[j]]
		if iAt != jAt {
			return iAt > jAt
		}
		return names[i] < names[j]
	})
	showedIPs := false
	if len(names) == 0 {
		out.Add(trh(lang, "online.noObservation"))
	} else {
		max := len(names)
		if max > 25 {
			max = 25
		}
		for _, name := range names[:max] {
			current := state.LiveIPs[name]
			live := fresh && len(current) > 0
			stamp := state.LastOnline[name]
			activeAt := state.LastActive[name]
			active := activeAt > 0 && now.Unix()-activeAt <= int64(onlineActivityWindow/time.Second)
			status := tr(lang, "online.recent")
			ips := state.OnlineIPs[name]
			if len(ips) == 0 {
				status = tr(lang, "online.notObserved")
			}
			if active && !live {
				status = tr(lang, "online.active")
			}
			if live {
				status = tr(lang, "online.live")
				ips = current
			}
			out.Add("👤 ", bold(name), " — ", esc(status), "\n")
			if stamp > 0 {
				out.Add(trh(lang, "online.lastSeen", time.Unix(stamp, 0).Format("2006-01-02 15:04:05")))
			} else {
				out.Add(trh(lang, "online.lastNever"))
			}
			limit := len(ips)
			if limit > 3 {
				limit = 3
			}
			if limit > 0 {
				showedIPs = true
			}
			for _, ip := range ips[:limit] {
				out.Add(trh(lang, "online.ip", ip.IP, time.Unix(ip.LastSeen, 0).Format("2006-01-02 15:04:05")))
			}
			usage := state.Users[name]
			out.Add(trh(lang, "online.traffic", FormatBytes(usage.TotalUp), FormatBytes(usage.TotalDown), FormatBytes(usage.TotalUp+usage.TotalDown)))
			out.Add("\n")
		}
		if len(names) > max {
			out.Add(trh(lang, "online.truncated"))
		}
	}
	if fresh {
		out.Add(trh(lang, "online.polled", time.Unix(state.OnlinePollAt, 0).Format("2006-01-02 15:04:05")))
	}
	out.Add(trh(lang, "online.totalScope"))
	// The spoofing caveat only concerns addresses that are actually displayed.
	if showedIPs {
		out.Add(trh(lang, "online.warning"))
	}
	return out.HTML()
}

func (s *BotService) sendOnlineCard(chatID int64, msgID int) {
	state, err := refreshOnlineTraffic(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer)
	// A failed live query can still show last stored observations. Errors
	// before loading the ledger must not masquerade as an empty online list.
	if state.Users == nil {
		detail := "unknown error"
		if err != nil {
			detail = err.Error()
		}
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "online.error", detail), s.backToMainMarkup())
		return
	}
	rows := [][]tgButton{
		newRow(newButton(tr(s.cfg.Language, "online.refresh"), "menu_online"), newButton(tr(s.cfg.Language, "online.doctor"), "menu_online_doctor")),
		newRow(newButton(tr(s.cfg.Language, "online.details"), "menu_users"), newButton(tr(s.cfg.Language, "menu.home"), "menu_main")),
	}
	text := onlineCardTextWithError(state, time.Now(), s.cfg.Language, err)
	// An empty online list is expected when a level that owns clients never
	// enabled statsUserOnline; say so and make the fix reachable in one tap.
	if missing, lookupErr := missingOnlineLevels(s.cfg.XrayConfigPath); lookupErr == nil && len(missing) > 0 {
		text = htmlText(strings.TrimRight(string(text), "\n")) + "\n" + trh(s.cfg.Language, "online.policyoff", strings.Join(missing, ", "))
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "stats.open"), "menu_stats")))
	}
	s.sendOrEdit(chatID, msgID, text, newKeyboard(rows...))
}
