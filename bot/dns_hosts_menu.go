package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func dnsHostsBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(
		newButton(tr(lang, "dns.hosts.back"), "dh:page:0"),
		newButton(tr(lang, "dns.back"), "dn:page:0")))
}

func dnsHostCallback(action string, index int, etag string) string {
	return fmt.Sprintf("dh:%s:%d:%s", action, index, etag)
}

func currentDNSHostsView(root map[string]any, etag string) (dnsHostsSummary, error) {
	view, err := dnsHostsView(root)
	if err != nil {
		return dnsHostsSummary{}, err
	}
	if len(etag) != 24 || view.ETag != etag {
		return dnsHostsSummary{}, errDNSChanged
	}
	return view, nil
}

func (s *BotService) sendDNSHostsPage(chatID int64, msgID, offset int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), dnsBackMarkup(s.cfg.Language))
		return
	}
	view, err := dnsHostsView(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), dnsBackMarkup(s.cfg.Language))
		return
	}
	if offset < 0 || len(view.Hosts) == 0 {
		offset = 0
	}
	if len(view.Hosts) > 0 && offset >= len(view.Hosts) {
		offset = ((len(view.Hosts) - 1) / routingPageSize) * routingPageSize
	}
	end := offset + routingPageSize
	if end > len(view.Hosts) {
		end = len(view.Hosts)
	}
	body := trh(s.cfg.Language, "dns.hosts.list", len(view.Hosts))
	if notice != "" {
		body += "\n" + esc(notice) + "\n"
	}
	var rows [][]tgButton
	for _, host := range view.Hosts[offset:end] {
		label := fmt.Sprintf("%d. %s", host.Index, host.Name)
		rows = append(rows, newRow(newButton(truncateLabel(label, 64), dnsHostCallback("v", host.Index, view.ETag))))
	}
	var pages []tgButton
	if offset > 0 {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("dh:page:%d", offset-routingPageSize)))
	}
	if end < len(view.Hosts) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), fmt.Sprintf("dh:page:%d", end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.hosts.add"), "dh:add:"+view.ETag)))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("dh:page:%d", offset)), newButton(tr(s.cfg.Language, "dns.back"), "dn:page:0")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendDNSHostDetail(chatID int64, msgID, index int, etag string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		var view dnsHostsSummary
		view, err = currentDNSHostsView(root, etag)
		if err == nil && index >= 1 && index <= len(view.Hosts) {
			host := view.Hosts[index-1]
			body := trh(s.cfg.Language, "dns.hosts.detail", host.Name, strings.Join(host.Targets, "\n"))
			var rows [][]tgButton
			if !host.Editable {
				body += trh(s.cfg.Language, "dns.hosts.advanced")
			} else if s.cfg.AllowMutations {
				rows = append(rows, newRow(
					newButton(tr(s.cfg.Language, "dns.hosts.edit"), dnsHostCallback("edit", index, etag)),
					newButton(tr(s.cfg.Language, "dns.hosts.rename"), dnsHostCallback("rename", index, etag))))
				rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.hosts.delete"), dnsHostCallback("delete", index, etag))))
			}
			offset := ((index - 1) / routingPageSize) * routingPageSize
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.hosts.back"), fmt.Sprintf("dh:page:%d", offset)), newButton(tr(s.cfg.Language, "dns.back"), "dn:page:0")))
			s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
			return
		}
	}
	s.sendDNSHostsPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
}

func (s *BotService) offerDNSHostChange(chatID, adminID int64, msgID int, change dnsHostChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), dnsHostsBackMarkup(s.cfg.Language))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyDNSHostChange(root, change)
	}
	if errors.Is(err, errDNSChanged) {
		s.sendDNSHostsPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
		return
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), dnsHostsBackMarkup(s.cfg.Language))
		return
	}
	var body htmlText
	switch change.Kind {
	case "add", "update":
		body = trh(s.cfg.Language, "dns.hosts.confirm.put", change.Name, strings.Join(change.Targets, ", "))
	case "rename":
		body = trh(s.cfg.Language, "dns.hosts.confirm.rename", change.Name, change.NewName)
	case "delete":
		body = trh(s.cfg.Language, "dns.hosts.confirm.delete", change.Name)
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{DNSHost: &change}, body)
}

func (s *BotService) handleDNSHostInput(msg *tgMessage, state dnsWizard) bool {
	if msg.Command() == "cancel" {
		clearDNSWizard(msg.Chat.ID)
		s.sendDNSHostsPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "wizard.cancelled"))
		return true
	}
	if msg.Command() != "" {
		return false
	}
	if !s.cfg.AllowMutations {
		clearDNSWizard(msg.Chat.ID)
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = currentDNSHostsView(root, state.ETag)
	}
	if errors.Is(err, errDNSChanged) {
		clearDNSWizard(msg.Chat.ID)
		s.sendDNSHostsPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "dns.changed"))
		return true
	}
	if err != nil {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	text := strings.TrimSpace(msg.Text)
	if state.Kind == "host-add-name" {
		if !safeDNSHostName(text) {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "dns.hosts.invalid_name"))
			return true
		}
		config, _ := readDNS(root)
		hosts, _ := readDNSHosts(config)
		if _, exists := hosts[text]; exists {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "dns.hosts.duplicate"))
			return true
		}
		state.Kind, state.Host = "host-add-targets", text
		setDNSWizard(msg.Chat.ID, state)
		s.sendOrEdit(msg.Chat.ID, 0, trh(s.cfg.Language, "dns.hosts.targets_prompt"), dnsHostsBackMarkup(s.cfg.Language))
		return true
	}
	change := dnsHostChange{ETag: state.ETag, Name: state.Host}
	switch state.Kind {
	case "host-add-targets", "host-update":
		change.Kind, change.Targets = "update", strings.Fields(text)
		if state.Kind == "host-add-targets" {
			change.Kind = "add"
		}
	case "host-rename":
		if !safeDNSHostName(text) {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "dns.hosts.invalid_name"))
			return true
		}
		change.Kind, change.NewName = "rename", text
	default:
		clearDNSWizard(msg.Chat.ID)
		return true
	}
	if _, err := applyDNSHostChange(root, change); err != nil {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	clearDNSWizard(msg.Chat.ID)
	s.offerDNSHostChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func (s *BotService) handleDNSHostCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	parts := strings.Split(cb.Data, ":")
	if len(parts) == 3 && parts[1] == "page" {
		offset, err := strconv.Atoi(parts[2])
		if err == nil {
			clearInputSessions(chatID)
			s.sendDNSHostsPage(chatID, msgID, offset, "")
		}
		return
	}
	if len(parts) != 3 && len(parts) != 4 {
		return
	}
	etag := parts[len(parts)-1]
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var view dnsHostsSummary
	if err == nil {
		view, err = currentDNSHostsView(root, etag)
	}
	if err != nil {
		s.sendDNSHostsPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
		return
	}
	var record dnsHostRecord
	if len(parts) == 4 {
		index, parseErr := strconv.Atoi(parts[2])
		if parseErr != nil || index < 1 || index > len(view.Hosts) {
			return
		}
		record = view.Hosts[index-1]
	}
	if parts[1] == "v" && len(parts) == 4 {
		clearInputSessions(chatID)
		s.sendDNSHostDetail(chatID, msgID, record.Index, etag)
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), dnsHostsBackMarkup(s.cfg.Language))
		return
	}
	if parts[1] == "add" && len(parts) == 3 {
		clearInputSessions(chatID)
		setDNSWizard(chatID, dnsWizard{AdminID: cb.From.ID, Kind: "host-add-name", ETag: etag, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "dns.hosts.name_prompt"), dnsHostsBackMarkup(s.cfg.Language))
		return
	}
	if len(parts) != 4 || !record.Editable {
		return
	}
	switch parts[1] {
	case "edit", "rename":
		clearInputSessions(chatID)
		kind, prompt := "host-update", "dns.hosts.targets_prompt"
		if parts[1] == "rename" {
			kind, prompt = "host-rename", "dns.hosts.name_prompt"
		}
		setDNSWizard(chatID, dnsWizard{AdminID: cb.From.ID, Kind: kind, ETag: etag, Host: record.Name, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, prompt), dnsHostsBackMarkup(s.cfg.Language))
	case "delete":
		s.offerDNSHostChange(chatID, cb.From.ID, msgID, dnsHostChange{Kind: "delete", ETag: etag, Name: record.Name})
	}
}
