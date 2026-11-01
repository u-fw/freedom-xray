package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func nativeClientCallback(action string, inbound, client int, etag string) string {
	return fmt.Sprintf("nc:%s:%d:%d:%s", action, inbound, client, etag)
}

func (s *BotService) returnNativeClientList(chatID int64, msgID, index int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		var values []any
		values, err = nativeInboundValues(root)
		if err == nil && index >= 1 && index <= len(values) {
			item, ok := getMap(values[index-1])
			if ok {
				etag, e := routingRuleETag(item)
				if e == nil {
					s.sendNativeClientList(chatID, msgID, index, 0, etag, notice)
					return
				}
			}
		}
	}
	s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.client.changed"))
}

func (s *BotService) sendNativeClientList(chatID int64, msgID, index, offset int, etag, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var inbound map[string]any
	var protocol string
	if err == nil {
		inbound, protocol, err = lookupNativeClientInbound(root, index, etag)
	}
	var records []nativeClientRecord
	if err == nil {
		records, err = nativeClientRecords(inbound)
	}
	if err != nil {
		s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.client.changed"))
		return
	}
	if offset < 0 || offset >= len(records) {
		offset = 0
	}
	end := offset + nodesPerPage
	if end > len(records) {
		end = len(records)
	}
	name, _ := inbound["tag"].(string)
	if name == "" {
		name = tr(s.cfg.Language, "inbound.native.untagged")
	}
	body := trh(s.cfg.Language, "inbound.client.list", safeInboundField(name), protocol, len(records))
	if notice != "" {
		body += "\n" + esc(notice)
	}
	var rows [][]tgButton
	for _, record := range records[offset:end] {
		rows = append(rows, newRow(newButton(truncateLabel(nativeClientDescription(record), 64), nativeClientCallback("view", index, record.Index, etag))))
	}
	var pages []tgButton
	if offset > 0 {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), nativeClientCallback("list", index, offset-nodesPerPage, etag)))
	}
	if end < len(records) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), nativeClientCallback("list", index, end, etag)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.client.add"), nativeClientCallback("add", index, 0, etag))))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.client.back_inbound"), nativeInboundCallback("v", index, etag))))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendNativeClientDetail(chatID int64, msgID, inboundIndex, clientIndex int, etag string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var inbound map[string]any
	var protocol string
	if err == nil {
		inbound, protocol, err = lookupNativeClientInbound(root, inboundIndex, etag)
	}
	var records []nativeClientRecord
	if err == nil {
		records, err = nativeClientRecords(inbound)
	}
	if err != nil || clientIndex < 1 || clientIndex > len(records) {
		s.returnNativeClientList(chatID, msgID, inboundIndex, tr(s.cfg.Language, "inbound.client.changed"))
		return
	}
	record := records[clientIndex-1]
	email := record.Email
	if email == "" {
		email = tr(s.cfg.Language, "inbound.client.unnamed")
	}
	body := trh(s.cfg.Language, "inbound.client.detail", clientIndex, protocol, email, record.Level)
	var rows [][]tgButton
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.client.export"), nativeClientCallback("export", inboundIndex, clientIndex, etag))))
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "inbound.client.email"), nativeClientCallback("email", inboundIndex, clientIndex, etag)),
			newButton(tr(s.cfg.Language, "inbound.client.level"), nativeClientCallback("level", inboundIndex, clientIndex, etag))))
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "inbound.client.rotate"), nativeClientCallback("rotate", inboundIndex, clientIndex, etag)),
			newButton(tr(s.cfg.Language, "inbound.client.remove"), nativeClientCallback("remove", inboundIndex, clientIndex, etag))))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.client.back_list"), nativeClientCallback("list", inboundIndex, 0, etag))))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) offerNativeClientChange(chatID, adminID int64, msgID int, change nativeClientChange) {
	if !s.cfg.AllowMutations {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.writesdisabled"))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil && (change.Kind == "email" || change.Kind == "remove") {
		change.RoutingETag, err = routingListETag(root)
	}
	var preview map[string]any
	if err == nil {
		preview, err = applyNativeClientChange(root, change, s.cfg)
	}
	if errors.Is(err, errNativeInboundChanged) || errors.Is(err, errRoutingRuleChanged) {
		s.returnNativeClientList(chatID, msgID, change.InboundIndex, tr(s.cfg.Language, "inbound.client.changed"))
		return
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), nativeInboundBackMarkup(s.cfg.Language))
		return
	}
	var detail htmlText
	if change.Kind == "add" || change.Kind == "email" || change.Kind == "level" {
		detail = "\n" + trh(s.cfg.Language, "inbound.client.new_value", change.Value)
	}
	if change.Kind == "rotate" {
		detail += "\n" + trh(s.cfg.Language, "inbound.client.rotate_warning")
	}
	if refs, ok := preview["routing_rules"].([]int); ok && len(refs) > 0 {
		parts := make([]string, 0, len(refs))
		for _, ref := range refs {
			parts = append(parts, strconv.Itoa(ref))
		}
		detail += "\n" + trh(s.cfg.Language, "inbound.client.routing_warning", strings.Join(parts, ", "))
	}
	target := tr(s.cfg.Language, "inbound.client.new")
	if change.ClientIndex > 0 {
		target = fmt.Sprintf("#%d", change.ClientIndex)
		if email, _ := preview["previous_email"].(string); email != "" {
			target += " " + email
		}
	}
	body := trh(s.cfg.Language, "inbound.client.confirm", tr(s.cfg.Language, "inbound.client."+change.Kind), change.InboundIndex, target, detail)
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{NativeClient: &change}, body)
}

func (s *BotService) handleNativeClientInput(msg *tgMessage, state nativeInboundWizard) bool {
	if msg.Command() == "cancel" {
		clearNativeInboundWizard(msg.Chat.ID)
		s.returnNativeClientList(msg.Chat.ID, 0, state.Index, tr(s.cfg.Language, "wizard.cancelled"))
		return true
	}
	if msg.Command() != "" {
		return false
	}
	if !s.cfg.AllowMutations {
		clearNativeInboundWizard(msg.Chat.ID)
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	change := nativeClientChange{Kind: strings.TrimPrefix(state.Kind, "client."), InboundIndex: state.Index, InboundETag: state.ETag, ClientIndex: state.ClientIndex, Value: strings.TrimSpace(msg.Text)}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyNativeClientChange(root, change, s.cfg)
	}
	if errors.Is(err, errNativeInboundChanged) {
		clearNativeInboundWizard(msg.Chat.ID)
		s.returnNativeClientList(msg.Chat.ID, 0, state.Index, tr(s.cfg.Language, "inbound.client.changed"))
		return true
	}
	if err != nil {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	clearNativeInboundWizard(msg.Chat.ID)
	s.offerNativeClientChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func (s *BotService) handleNativeClientCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	parts := strings.Split(cb.Data, ":")
	if len(parts) != 5 {
		return
	}
	inboundIndex, err1 := strconv.Atoi(parts[2])
	clientIndex, err2 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil || inboundIndex < 1 || clientIndex < 0 {
		return
	}
	chatID, msgID, etag := cb.Message.Chat.ID, cb.Message.MessageID, parts[4]
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var inbound map[string]any
	if err == nil {
		inbound, _, err = lookupNativeClientInbound(root, inboundIndex, etag)
	}
	if err == nil && parts[1] != "list" && parts[1] != "add" {
		_, err = lookupNativeClient(inbound, clientIndex)
	}
	if err != nil {
		s.returnNativeClientList(chatID, msgID, inboundIndex, tr(s.cfg.Language, "inbound.client.changed"))
		return
	}
	switch parts[1] {
	case "list":
		clearInputSessions(chatID)
		s.sendNativeClientList(chatID, msgID, inboundIndex, clientIndex, etag, "")
	case "view":
		clearInputSessions(chatID)
		s.sendNativeClientDetail(chatID, msgID, inboundIndex, clientIndex, etag)
	case "export":
		client, err := lookupNativeClient(inbound, clientIndex)
		if err != nil {
			return
		}
		data, err := json.MarshalIndent(client, "", "  ")
		if err != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "inbound.client.export_failed"))
			return
		}
		if err := s.tg.sendDocument(chatID, "xray-client.json", append(data, '\n'), tr(s.cfg.Language, "inbound.client.export_caption")); err != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "inbound.client.export_failed"))
		}
	case "add", "email", "level":
		if !s.cfg.AllowMutations {
			return
		}
		clearInputSessions(chatID)
		setNativeInboundWizard(chatID, nativeInboundWizard{AdminID: cb.From.ID, Kind: "client." + parts[1], Index: inboundIndex, ClientIndex: clientIndex, ETag: etag, Expires: time.Now().Add(5 * time.Minute)})
		prompt := "inbound.client.email_prompt"
		if parts[1] == "level" {
			prompt = "inbound.client.level_prompt"
		}
		markup := newKeyboard(newRow(newButton(tr(s.cfg.Language, "inbound.client.back_list"), nativeClientCallback("list", inboundIndex, 0, etag))))
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, prompt), markup)
	case "rotate", "remove":
		s.offerNativeClientChange(chatID, cb.From.ID, msgID, nativeClientChange{Kind: parts[1], InboundIndex: inboundIndex, InboundETag: etag, ClientIndex: clientIndex})
	}
}
