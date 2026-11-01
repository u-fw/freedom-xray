package main

import (
	"context"
	"strings"
	"time"
)

// Keep common tasks on the dashboard and put less frequent service settings
// together. Existing command and callback names continue to work.
func mainMenuMarkup(lang string) tgKeyboard {
	return newKeyboard(
		newRow(newButton(tr(lang, "menu.users"), "menu_users"), newButton(tr(lang, "menu.nodes"), "menu_nodes")),
		newRow(newButton(tr(lang, "menu.traffic"), "menu_traffic"), newButton(tr(lang, "menu.online"), "menu_online")),
		newRow(newButton(tr(lang, "menu.routing"), "menu_routing"), newButton(tr(lang, "menu.status"), "menu_status")),
		newRow(newButton(tr(lang, "menu.tools"), "menu_tools"), newButton(tr(lang, "menu.refresh"), "menu_main")),
	)
}

func (s *BotService) sendToolsCard(chatID int64, msgID int) {
	lang := s.cfg.Language
	markup := newKeyboard(
		newRow(newButton(tr(lang, "menu.update"), "menu_update_center"), newButton(tr(lang, "menu.version"), "menu_version")),
		newRow(newButton(tr(lang, "menu.stats"), "menu_stats"), newButton(tr(lang, "menu.onlinedoctor"), "menu_online_doctor")),
		newRow(newButton(tr(lang, "serverhost.button"), "routing_manage:server_host"), newButton(tr(lang, "menu.backup"), "menu_backup")),
		newRow(newButton(tr(lang, "xray.section.menu"), "xs:page")),
		newRow(newButton(languageButtonLabel("en", lang), "lang_set:en"), newButton(languageButtonLabel("zh-CN", lang), "lang_set:zh-CN")),
		newRow(newButton(tr(lang, "menu.help"), "menu_help"), newButton(tr(lang, "menu.home"), "menu_main")),
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
	keyboard := newKeyboard(
		newRow(newButton(tr(s.cfg.Language, "version.updatecenter"), "menu_update_center")),
		newRow(newButton(tr(s.cfg.Language, "version.refresh"), "menu_version"), newButton(tr(s.cfg.Language, "version.mainmenu"), "menu_main")),
	)
	s.sendOrEdit(chatID, msgID, body.String(), keyboard)
}

func (s *BotService) sendHelpCard(chatID int64, msgID int) {
	msg := tr(s.cfg.Language, "help.body")
	markup := newKeyboard(
		newRow(newButton(tr(s.cfg.Language, "menu.users"), "menu_users"), newButton(tr(s.cfg.Language, "help.update"), "menu_update_center")),
		newRow(newButton(tr(s.cfg.Language, "menu.home"), "menu_main")),
	)
	s.sendOrEdit(chatID, msgID, msg, markup)
}
