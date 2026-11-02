package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type outboundWizard struct {
	AdminID  int64
	Step     string
	Index    int
	ETag     string
	ListETag string
	Protocol string
	Expires  time.Time
}

var outboundWizards = struct {
	sync.Mutex
	items map[int64]outboundWizard
}{items: map[int64]outboundWizard{}}

func clearOutboundWizard(chatID int64) {
	outboundWizards.Lock()
	delete(outboundWizards.items, chatID)
	outboundWizards.Unlock()
}

func setOutboundWizard(chatID int64, state outboundWizard) {
	outboundWizards.Lock()
	outboundWizards.items[chatID] = state
	outboundWizards.Unlock()
}

func getOutboundWizard(chatID, adminID int64) (outboundWizard, bool) {
	outboundWizards.Lock()
	defer outboundWizards.Unlock()
	state, ok := outboundWizards.items[chatID]
	if ok && time.Now().After(state.Expires) {
		delete(outboundWizards.items, chatID)
		return outboundWizard{}, false
	}
	return state, ok && state.AdminID == adminID
}

func outboundCallback(action string, index int, etag string) string {
	return fmt.Sprintf("ob:%s:%d:%s", action, index, etag)
}

func outboundBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(
		newButton(tr(lang, "outbound.back"), "ob:page:0"),
		newButton(tr(lang, "routing.guide.back"), "rt:page:0")))
}

func (s *BotService) sendOutboundPage(chatID int64, msgID, offset int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), routingPageBackMarkup(s.cfg.Language))
		return
	}
	records, err := outboundRecords(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), routingPageBackMarkup(s.cfg.Language))
		return
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(records) {
		offset = ((len(records) - 1) / routingPageSize) * routingPageSize
	}
	end := offset + routingPageSize
	if end > len(records) {
		end = len(records)
	}
	body := trh(s.cfg.Language, "outbound.list", len(records))
	if notice != "" {
		body += "\n" + esc(notice) + "\n"
	}
	var rows [][]tgButton
	for _, record := range records[offset:end] {
		tag := record.Tag
		if tag == "" {
			tag = tr(s.cfg.Language, "outbound.unnamed")
		}
		label := fmt.Sprintf("%d. %s · %s", record.Index, tag, record.Protocol)
		if record.Default {
			label = "⭐ " + label
		}
		rows = append(rows, newRow(newButton(truncateLabel(label, 64), outboundCallback("v", record.Index, record.ETag))))
	}
	var pages []tgButton
	if offset > 0 {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("ob:page:%d", offset-routingPageSize)))
	}
	if end < len(records) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), fmt.Sprintf("ob:page:%d", end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "outbound.add"), "ob:add")))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("ob:page:%d", offset)), newButton(tr(s.cfg.Language, "routing.guide.back"), "rt:page:0")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendOutboundDetail(chatID int64, msgID, index int, etag string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = lookupOutbound(root, index, etag)
	}
	if err != nil {
		s.sendOutboundPage(chatID, msgID, 0, tr(s.cfg.Language, "outbound.changed"))
		return
	}
	records, err := outboundRecords(root)
	if err != nil {
		s.sendOutboundPage(chatID, msgID, 0, tr(s.cfg.Language, "outbound.changed"))
		return
	}
	record := records[index-1]
	tag := record.Tag
	if tag == "" {
		tag = tr(s.cfg.Language, "outbound.unnamed")
	}
	state := tr(s.cfg.Language, "outbound.regular")
	if record.Default {
		state = tr(s.cfg.Language, "outbound.default")
	}
	body := trh(s.cfg.Language, "outbound.detail", record.Index, tag, record.Protocol, state)
	if record.Endpoint != nil {
		body += trh(s.cfg.Language, "outbound.endpoint", record.Endpoint.Address, record.Endpoint.Port)
	}
	body += trh(s.cfg.Language, "outbound.hidden")
	var rows [][]tgButton
	protected := record.Tag != "" && record.Tag == networkAPITag(root)
	if s.cfg.AllowMutations && !protected {
		if record.Endpoint != nil {
			rows = append(rows, newRow(
				newButton(tr(s.cfg.Language, "outbound.edit.address"), outboundCallback("address", index, etag)),
				newButton(tr(s.cfg.Language, "outbound.edit.port"), outboundCallback("port", index, etag))))
		}
		if !record.Default {
			rows = append(rows, newRow(
				newButton(tr(s.cfg.Language, "outbound.set_default"), outboundCallback("default", index, etag)),
				newButton(tr(s.cfg.Language, "outbound.delete"), outboundCallback("delete", index, etag))))
		}
	}
	if protected {
		body += trh(s.cfg.Language, "routing.guide.protected")
	}
	if s.cfg.AllowSensitiveBackup {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "config.export.button"), nativeConfigExportCallback("outbound", index, etag))))
	}
	offset := ((index - 1) / routingPageSize) * routingPageSize
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "outbound.back"), fmt.Sprintf("ob:page:%d", offset)), newButton(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) offerOutboundChange(chatID, adminID int64, msgID int, change outboundChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), outboundBackMarkup(s.cfg.Language))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var before string
	if err == nil && (change.Kind == "address" || change.Kind == "port") {
		item, e := lookupOutbound(root, change.Index, change.ETag)
		if e != nil {
			err = e
		} else if endpoint := outboundEndpointInfo(item); endpoint != nil {
			before = endpoint.Address
			if change.Kind == "port" {
				before = strconv.Itoa(endpoint.Port)
			}
		}
	}
	if err == nil {
		_, err = applyOutboundChange(root, change)
	}
	if errors.Is(err, errOutboundChanged) {
		s.sendOutboundPage(chatID, msgID, 0, tr(s.cfg.Language, "outbound.changed"))
		return
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), outboundBackMarkup(s.cfg.Language))
		return
	}
	var body htmlText
	switch change.Kind {
	case "create":
		body = trh(s.cfg.Language, "outbound.confirm.create", change.Tag, change.Protocol)
	case "address", "port":
		body = trh(s.cfg.Language, "outbound.confirm.field", change.Index, tr(s.cfg.Language, "outbound.edit."+change.Kind), before, change.Value)
	case "default":
		body = trh(s.cfg.Language, "outbound.confirm.default", change.Index)
	case "delete":
		body = trh(s.cfg.Language, "outbound.confirm.delete", change.Index)
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{Outbound: &change}, body)
}

func (s *BotService) handleOutboundInput(msg *tgMessage) bool {
	if msg.Chat == nil || msg.From == nil {
		return false
	}
	state, ok := getOutboundWizard(msg.Chat.ID, msg.From.ID)
	if !ok {
		return false
	}
	if msg.Command() == "cancel" {
		clearOutboundWizard(msg.Chat.ID)
		s.sendOutboundPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "wizard.cancelled"))
		return true
	}
	if msg.Command() != "" {
		return false
	}
	if !s.cfg.AllowMutations {
		clearOutboundWizard(msg.Chat.ID)
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	value := strings.TrimSpace(msg.Text)
	change := outboundChange{Kind: state.Step, Index: state.Index, ETag: state.ETag, Value: value}
	if state.Step == "create" {
		change.Tag, change.Protocol, change.ListETag = value, state.Protocol, state.ListETag
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyOutboundChange(root, change)
	}
	if errors.Is(err, errOutboundChanged) {
		clearOutboundWizard(msg.Chat.ID)
		s.sendOutboundPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "outbound.changed"))
		return true
	}
	if err != nil {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	clearOutboundWizard(msg.Chat.ID)
	s.offerOutboundChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func (s *BotService) handleOutboundCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	parts := strings.Split(cb.Data, ":")
	if len(parts) < 2 {
		return
	}
	if parts[1] == "page" && len(parts) == 3 {
		offset, err := strconv.Atoi(parts[2])
		if err == nil {
			clearInputSessions(chatID)
			s.sendOutboundPage(chatID, msgID, offset, "")
		}
		return
	}
	if parts[1] == "add" && len(parts) == 2 {
		if !s.cfg.AllowMutations {
			s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), outboundBackMarkup(s.cfg.Language))
			return
		}
		rows := [][]tgButton{
			newRow(newButton(tr(s.cfg.Language, "outbound.create.freedom"), "ob:new:freedom"), newButton(tr(s.cfg.Language, "outbound.create.blackhole"), "ob:new:blackhole")),
			newRow(newButton(tr(s.cfg.Language, "outbound.back"), "ob:page:0")),
		}
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "outbound.add_picker"), newKeyboard(rows...))
		return
	}
	if parts[1] == "new" && len(parts) == 3 {
		if !s.cfg.AllowMutations || (parts[2] != "freedom" && parts[2] != "blackhole") {
			return
		}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendOutboundPage(chatID, msgID, 0, tr(s.cfg.Language, "outbound.changed"))
			return
		}
		listETag, err := outboundListETag(root)
		if err != nil {
			s.sendOutboundPage(chatID, msgID, 0, tr(s.cfg.Language, "outbound.changed"))
			return
		}
		clearInputSessions(chatID)
		setOutboundWizard(chatID, outboundWizard{AdminID: cb.From.ID, Step: "create", Protocol: parts[2], ListETag: listETag, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "outbound.tag_prompt", parts[2]), outboundBackMarkup(s.cfg.Language))
		return
	}
	if len(parts) != 4 {
		return
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 1 || len(parts[3]) != 24 {
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = lookupOutbound(root, index, parts[3])
	}
	if err != nil {
		s.sendOutboundPage(chatID, msgID, 0, tr(s.cfg.Language, "outbound.changed"))
		return
	}
	if parts[1] == "v" {
		clearInputSessions(chatID)
		s.sendOutboundDetail(chatID, msgID, index, parts[3])
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), outboundBackMarkup(s.cfg.Language))
		return
	}
	switch parts[1] {
	case "address", "port":
		clearInputSessions(chatID)
		setOutboundWizard(chatID, outboundWizard{AdminID: cb.From.ID, Step: parts[1], Index: index, ETag: parts[3], Expires: time.Now().Add(5 * time.Minute)})
		prompt := "outbound.address_prompt"
		if parts[1] == "port" {
			prompt = "outbound.port_prompt"
		}
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, prompt), outboundBackMarkup(s.cfg.Language))
	case "default":
		listETag, err := outboundListETag(root)
		if err != nil {
			return
		}
		s.offerOutboundChange(chatID, cb.From.ID, msgID, outboundChange{Kind: "default", Index: index, ETag: parts[3], ListETag: listETag})
	case "delete":
		s.offerOutboundChange(chatID, cb.From.ID, msgID, outboundChange{Kind: "delete", Index: index, ETag: parts[3]})
	}
}
