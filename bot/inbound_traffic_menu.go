package main

import (
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (s *BotService) sendInboundTrafficCard(chatID int64, msgID int) {
	state, err := snapshotTraffic(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer)
	if err != nil {
		cached, readErr := loadTrafficStore(s.cfg.XrayConfigPath)
		if readErr != nil {
			s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "inbound.failure", mdEsc(err.Error()), mdEsc(readErr.Error())), s.backToMainMarkup())
			return
		}
		state = cached
	}
	traffic := storedInboundTraffic(state)
	var body strings.Builder
	body.WriteString(tr(s.cfg.Language, "inbound.title"))
	if err != nil {
		body.WriteString(tr(s.cfg.Language, "inbound.stale", mdEsc(err.Error())))
	}
	report, reportErr := inspectStatsSwitches(s.cfg.XrayConfigPath, s.cfg.StatsAPIServer)
	inboundOff := reportErr == nil && !report.SystemInbound
	var total uint64
	if len(traffic) == 0 {
		if inboundOff {
			body.WriteString(tr(s.cfg.Language, "stats.inbound.off"))
		} else {
			body.WriteString(tr(s.cfg.Language, "inbound.empty"))
		}
	}
	for _, entry := range traffic {
		total += entry.Total()
		body.WriteString(tr(s.cfg.Language, "inbound.row", mdEsc(entry.Tag), FormatBytes(entry.Uplink), FormatBytes(entry.Downlink), FormatBytes(entry.Total())))
	}
	for _, entry := range traffic {
		entries := storedBindingTrafficForTag(state, entry.Tag)
		if len(entries) == 0 {
			continue
		}
		body.WriteString(tr(s.cfg.Language, "inbound.bindings.header", mdEsc(entry.Tag)))
		limit := len(entries)
		if limit > 3 {
			limit = 3
		}
		for _, item := range entries[:limit] {
			body.WriteString(tr(s.cfg.Language, "inbound.bindings.row", mdEsc(item.User), FormatBytes(item.Uplink), FormatBytes(item.Downlink), FormatBytes(item.Total())))
		}
		if len(entries) > limit {
			body.WriteString(tr(s.cfg.Language, "inbound.bindings.more", len(entries)-limit))
		}
	}
	body.WriteString(tr(s.cfg.Language, "inbound.total", FormatBytes(total)))
	body.WriteString(tr(s.cfg.Language, "inbound.note"))
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.refresh"), "menu_inbound_traffic"), tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.users"), "menu_traffic")),
	}
	if inboundOff {
		if len(traffic) > 0 {
			body.WriteString(tr(s.cfg.Language, "stats.inbound.off"))
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "stats.open"), "menu_stats")))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, body.String(), tgbotapi.NewInlineKeyboardMarkup(rows...))
}
