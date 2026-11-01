package main

import (
	"context"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Keep common tasks on the dashboard and put less frequent service settings
// together. Existing command and callback names continue to work.
func mainMenuMarkup(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.users"), "menu_users"), tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.nodes"), "menu_nodes")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.traffic"), "menu_traffic"), tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.online"), "menu_online")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.routing"), "menu_routing"), tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.status"), "menu_status")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.tools"), "menu_tools"), tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.refresh"), "menu_main")),
	)
}

func (s *BotService) sendToolsCard(chatID int64, msgID int) {
	lang := s.cfg.Language
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.update"), "menu_update_center"), tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.version"), "menu_version")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.stats"), "menu_stats"), tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.onlinedoctor"), "menu_online_doctor")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(lang, "serverhost.button"), "routing_manage:server_host"), tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.backup"), "menu_backup")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(languageButtonLabel("en", lang), "lang_set:en"), tgbotapi.NewInlineKeyboardButtonData(languageButtonLabel("zh-CN", lang), "lang_set:zh-CN")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.help"), "menu_help"), tgbotapi.NewInlineKeyboardButtonData(tr(lang, "menu.home"), "menu_main")),
	)
	s.sendOrEdit(chatID, msgID, tr(lang, "menu.tools.body"), markup)
}

// Version diagnostics are read-only and remain available even when updates are disabled.
func (s *BotService) sendVersionCard(chatID int64, msgID int) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var body strings.Builder
	body.WriteString(tr(s.cfg.Language, "version.title"))
	for _, component := range []string{"script", "bot", "core"} {
		local := currentComponentVersion(component, s.cfg.XrayBinPath)
		remote, err := remoteComponentVersion(ctx, component)
		if err != nil {
			remote = tr(s.cfg.Language, "version.fetchfailed", err.Error())
		}
		if len(remote) == 40 {
			remote = remote[:12]
		}
		body.WriteString(tr(s.cfg.Language, "version.component", componentLabel(component), mdCode(local), mdCode(remote)))
	}
	if s.cfg.AllowUpdates {
		body.WriteString(tr(s.cfg.Language, "version.updates.allowed"))
	} else {
		body.WriteString(tr(s.cfg.Language, "version.updates.disabled"))
	}
	if s.cfg.AllowCoreUpdates && s.cfg.AllowUpdates {
		body.WriteString(tr(s.cfg.Language, "version.core.allowed"))
	} else {
		body.WriteString(tr(s.cfg.Language, "version.core.disabled"))
	}
	body.WriteString(tr(s.cfg.Language, "version.note"))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "version.updatecenter"), "menu_update_center")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "version.refresh"), "menu_version"), tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "version.mainmenu"), "menu_main")),
	)
	s.sendOrEdit(chatID, msgID, body.String(), keyboard)
}

func (s *BotService) sendHelpCard(chatID int64, msgID int) {
	msg := tr(s.cfg.Language, "help.body")
	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "menu.users"), "menu_users"), tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "help.update"), "menu_update_center")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "menu.home"), "menu_main")),
	)
	s.sendOrEdit(chatID, msgID, msg, markup)
}
