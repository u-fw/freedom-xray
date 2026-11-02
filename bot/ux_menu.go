package main

import (
	"context"
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

// toolsNavRow leads back to the Tools card that lists a screen, or home.
func toolsNavRow(lang string) []tgButton {
	return newRow(newButton(tr(lang, "common.back"), "menu_tools"), newButton(tr(lang, "menu.home"), "menu_main"))
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
	s.sendOrEdit(chatID, msgID, trh(lang, "menu.tools.body"), markup)
}

// Version diagnostics are read-only and remain available even when updates are disabled.
func (s *BotService) sendVersionCard(chatID int64, msgID int) {
	s.renderLater(chatID, msgID, trh(s.cfg.Language, "update.checking"), s.versionScreen)
}

func (s *BotService) versionScreen() (htmlText, tgKeyboard) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var body htmlBuilder
	body.Add(trh(s.cfg.Language, "version.title"))
	for _, component := range []string{"script", "bot", "core"} {
		local := currentComponentVersion(component, s.cfg.XrayBinPath)
		remote, err := remoteComponentVersion(ctx, component)
		if err != nil {
			remote = tr(s.cfg.Language, "version.fetchfailed", err.Error())
		}
		if len(remote) == 40 {
			remote = remote[:12]
		}
		body.Add(trh(s.cfg.Language, "version.component", componentLabel(component), local, remote))
	}
	if s.cfg.AllowUpdates {
		body.Add(trh(s.cfg.Language, "version.updates.allowed"))
	} else {
		body.Add(trh(s.cfg.Language, "version.updates.disabled"))
	}
	if s.cfg.AllowCoreUpdates && s.cfg.AllowUpdates {
		body.Add(trh(s.cfg.Language, "version.core.allowed"))
	} else {
		body.Add(trh(s.cfg.Language, "version.core.disabled"))
	}
	body.Add(trh(s.cfg.Language, "version.note"))
	keyboard := newKeyboard(
		newRow(newButton(tr(s.cfg.Language, "version.updatecenter"), "menu_update_center")),
		newRow(newButton(tr(s.cfg.Language, "version.refresh"), "menu_version")),
		toolsNavRow(s.cfg.Language),
	)
	return body.HTML(), keyboard
}

func (s *BotService) sendHelpCard(chatID int64, msgID int) {
	msg := trh(s.cfg.Language, "help.body")
	markup := newKeyboard(
		newRow(newButton(tr(s.cfg.Language, "menu.users"), "menu_users"), newButton(tr(s.cfg.Language, "help.update"), "menu_update_center")),
		toolsNavRow(s.cfg.Language),
	)
	s.sendOrEdit(chatID, msgID, msg, markup)
}
