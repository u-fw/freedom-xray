package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func xraySectionCallback(action, name, etag string) string {
	return fmt.Sprintf("xs:%s:%s:%s", action, name, etag)
}

func xraySectionsBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(newButton(tr(lang, "xray.section.back"), "xs:page")))
}

func (s *BotService) sendXraySectionsPage(chatID int64, msgID int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var records []xraySectionRecord
	if err == nil {
		records, err = xraySectionRecords(root)
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), s.backToMainMarkup())
		return
	}
	body := trh(s.cfg.Language, "xray.section.list")
	if notice != "" {
		body += "\n" + esc(notice)
	}
	var rows [][]tgButton
	for _, record := range records {
		state := tr(s.cfg.Language, "xray.section.absent")
		if record.Configured {
			state = tr(s.cfg.Language, "xray.section.configured")
		}
		label := tr(s.cfg.Language, "xray.section."+record.Name) + " · " + state
		rows = append(rows, newRow(newButton(label, xraySectionCallback("v", record.Name, record.ETag))))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "menu.tools"), "menu_tools"), newButton(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendXraySectionDetail(chatID int64, msgID int, name, etag, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var record xraySectionRecord
	if err == nil {
		record, err = xraySectionInfo(root, name)
		if err == nil && record.ETag != etag {
			err = errXraySectionChanged
		}
	}
	if err != nil {
		s.sendXraySectionsPage(chatID, msgID, tr(s.cfg.Language, "xray.section.changed"))
		return
	}
	state := tr(s.cfg.Language, "xray.section.absent")
	if record.Configured {
		state = tr(s.cfg.Language, "xray.section.configured")
	}
	body := trh(s.cfg.Language, "xray.section.detail", tr(s.cfg.Language, "xray.section."+name), state)
	if value, _, e := lookupXraySection(root, name, etag); e == nil && record.Configured {
		summary, _ := json.MarshalIndent(xraySectionSafeSummary(name, value), "", "  ")
		body += "\n" + pre(string(summary)) + "\n"
	}
	body += "\n" + trh(s.cfg.Language, "xray.section.note."+name)
	if notice != "" {
		body += "\n" + esc(notice)
	}
	var rows [][]tgButton
	if record.Configured {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "config.export.button"), nativeConfigExportCallback("section", xraySectionIndex(name), etag))))
	}
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "xray.section.edit"), xraySectionCallback("put", name, etag))))
		if record.Configured {
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "xray.section.reset"), xraySectionCallback("reset", name, etag))))
		}
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "xray.section.back"), "xs:page")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) returnXraySection(chatID int64, msgID int, name, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		record, e := xraySectionInfo(root, name)
		if e == nil {
			s.sendXraySectionDetail(chatID, msgID, name, record.ETag, notice)
			return
		}
	}
	s.sendXraySectionsPage(chatID, msgID, tr(s.cfg.Language, "xray.section.changed"))
}

func (s *BotService) offerXraySectionChange(chatID, adminID int64, msgID int, change xraySectionChange) {
	if !s.cfg.AllowMutations {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.writesdisabled"))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyXraySectionChange(root, change)
	}
	if errors.Is(err, errXraySectionChanged) {
		s.returnXraySection(chatID, msgID, change.Name, tr(s.cfg.Language, "xray.section.changed"))
		return
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), xraySectionsBackMarkup(s.cfg.Language))
		return
	}
	body := trh(s.cfg.Language, "xray.section.confirm", tr(s.cfg.Language, "xray.section."+change.Name), tr(s.cfg.Language, "xray.section.action."+change.Kind))
	if change.Name == "policy" || (change.Name == "fakedns" && change.Kind == "reset") {
		body += "\n" + trh(s.cfg.Language, "xray.section.note."+change.Name)
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{XraySection: &change}, body)
}

func (s *BotService) handleXraySectionInput(msg *tgMessage, state routingWizard) bool {
	if msg.Command() == "cancel" {
		consumeRoutingWizard(msg.Chat.ID, state)
		s.returnXraySection(msg.Chat.ID, 0, state.Section, tr(s.cfg.Language, "wizard.cancelled"))
		return true
	}
	if msg.Command() != "" {
		return false
	}
	if !s.cfg.AllowMutations {
		consumeRoutingWizard(msg.Chat.ID, state)
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	input := []byte(msg.Text)
	if msg.Document != nil {
		var err error
		input, err = readTelegramJSONDocument(s.tg, msg.Document)
		if err != nil {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
			return true
		}
	}
	value, err := decodeXraySectionValue(state.Section, input)
	if err != nil {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	change := xraySectionChange{Kind: "put", Name: state.Section, ETag: state.ETag, Value: value}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyXraySectionChange(root, change)
	}
	if errors.Is(err, errXraySectionChanged) {
		consumeRoutingWizard(msg.Chat.ID, state)
		s.returnXraySection(msg.Chat.ID, 0, state.Section, tr(s.cfg.Language, "xray.section.changed"))
		return true
	}
	if err != nil {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	if consumeRoutingWizard(msg.Chat.ID, state) {
		s.offerXraySectionChange(msg.Chat.ID, msg.From.ID, 0, change)
	}
	return true
}

func (s *BotService) handleXraySectionCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	if cb.Data == "xs:page" {
		clearOtherRoutingInputs(chatID)
		s.sendXraySectionsPage(chatID, msgID, "")
		return
	}
	parts := strings.Split(cb.Data, ":")
	if len(parts) != 4 || xraySectionIndex(parts[2]) == 0 {
		return
	}
	name, etag := parts[2], parts[3]
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, _, err = lookupXraySection(root, name, etag)
	}
	if err != nil {
		s.returnXraySection(chatID, msgID, name, tr(s.cfg.Language, "xray.section.changed"))
		return
	}
	switch parts[1] {
	case "v":
		clearRoutingWizard(chatID)
		s.sendXraySectionDetail(chatID, msgID, name, etag, "")
	case "put":
		if !s.cfg.AllowMutations {
			return
		}
		clearOtherRoutingInputs(chatID)
		routingWizards.Lock()
		routingWizards.items[chatID] = routingWizard{AdminID: cb.From.ID, Kind: "xray_section", Section: name, ETag: etag, Expires: time.Now().Add(5 * time.Minute)}
		routingWizards.Unlock()
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "xray.section.prompt", tr(s.cfg.Language, "xray.section."+name)), xraySectionsBackMarkup(s.cfg.Language))
	case "reset":
		s.offerXraySectionChange(chatID, cb.From.ID, msgID, xraySectionChange{Kind: "reset", Name: name, ETag: etag})
	}
}
