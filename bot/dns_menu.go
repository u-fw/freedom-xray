package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type dnsWizard struct {
	AdminID int64
	Kind    string
	Index   int
	ETag    string
	Host    string
	Expires time.Time
}

var dnsWizards = struct {
	sync.Mutex
	items map[int64]dnsWizard
}{items: map[int64]dnsWizard{}}

var dnsFlagCodes = map[string]string{
	"cache": "disableCache", "fallback": "disableFallback", "match": "disableFallbackIfMatch",
	"parallel": "enableParallelQuery", "hosts": "useSystemHosts",
}

func clearDNSWizard(chatID int64) {
	dnsWizards.Lock()
	delete(dnsWizards.items, chatID)
	dnsWizards.Unlock()
}

func setDNSWizard(chatID int64, state dnsWizard) {
	dnsWizards.Lock()
	dnsWizards.items[chatID] = state
	dnsWizards.Unlock()
}

func getDNSWizard(chatID, adminID int64) (dnsWizard, bool) {
	dnsWizards.Lock()
	defer dnsWizards.Unlock()
	state, ok := dnsWizards.items[chatID]
	if ok && time.Now().After(state.Expires) {
		delete(dnsWizards.items, chatID)
		return dnsWizard{}, false
	}
	return state, ok && state.AdminID == adminID
}

func dnsBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(
		newButton(tr(lang, "dns.back"), "dn:page:0"),
		newButton(tr(lang, "routing.guide.back"), "rt:page:0")))
}

func dnsCallback(action string, index int, etag string) string {
	return fmt.Sprintf("dn:%s:%d:%s", action, index, etag)
}

func currentDNSView(root map[string]any, etag string) (dnsSummary, error) {
	view, err := dnsView(root)
	if err != nil {
		return dnsSummary{}, err
	}
	if len(etag) != 24 || view.ETag != etag {
		return dnsSummary{}, errDNSChanged
	}
	return view, nil
}

func dnsSwitchLabel(lang, code string, value bool) string {
	if code == "cache" || code == "fallback" {
		value = !value
	}
	state := tr(lang, "dns.switch.off")
	if value {
		state = tr(lang, "dns.switch.on")
	}
	return tr(lang, "dns.switch."+code, state)
}

func (s *BotService) sendDNSPage(chatID int64, msgID, offset int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), routingPageBackMarkup(s.cfg.Language))
		return
	}
	view, err := dnsView(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), routingPageBackMarkup(s.cfg.Language))
		return
	}
	if offset < 0 || len(view.Servers) == 0 {
		offset = 0
	}
	if len(view.Servers) > 0 && offset >= len(view.Servers) {
		offset = ((len(view.Servers) - 1) / routingPageSize) * routingPageSize
	}
	end := offset + routingPageSize
	if end > len(view.Servers) {
		end = len(view.Servers)
	}
	strategy := view.QueryStrategy
	if strategy == "" {
		strategy = tr(s.cfg.Language, "node.default")
	}
	body := trh(s.cfg.Language, "dns.list", len(view.Servers), strategy)
	if !view.Configured {
		body += trh(s.cfg.Language, "dns.notconfigured")
	}
	if view.ScriptConfigured {
		body += trh(s.cfg.Language, "dns.script")
	}
	if notice != "" {
		body += "\n" + esc(notice) + "\n"
	}
	var rows [][]tgButton
	for _, server := range view.Servers[offset:end] {
		label := fmt.Sprintf("%d. %s", server.Index, server.Address)
		rows = append(rows, newRow(newButton(truncateLabel(label, 64), dnsCallback("v", server.Index, view.ETag))))
	}
	var pages []tgButton
	if offset > 0 {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("dn:page:%d", offset-routingPageSize)))
	}
	if end < len(view.Servers) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), fmt.Sprintf("dn:page:%d", end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.hosts.button"), "dh:page:0")))
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.add"), "dn:add:"+view.ETag)))
		if view.Configured && !view.ScriptConfigured {
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.strategy"), "dn:strategy:"+view.ETag)))
			for _, pair := range [][]string{{"cache", "fallback"}, {"match"}, {"parallel", "hosts"}} {
				var buttons []tgButton
				for _, code := range pair {
					buttons = append(buttons, newButton(dnsSwitchLabel(s.cfg.Language, code, view.Flags[dnsFlagCodes[code]]), "dn:f:"+code+":"+view.ETag))
				}
				rows = append(rows, buttons)
			}
		}
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.advanced"), "routing_manage:dns_put")))
	}
	if view.Configured {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "config.export.button"), nativeConfigExportCallback("dns", 0, view.ETag))))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("dn:page:%d", offset)), newButton(tr(s.cfg.Language, "routing.guide.back"), "rt:page:0")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendDNSServerDetail(chatID int64, msgID, index int, etag string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendDNSPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
		return
	}
	view, err := currentDNSView(root, etag)
	if err != nil || index < 1 || index > len(view.Servers) {
		s.sendDNSPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
		return
	}
	server := view.Servers[index-1]
	body := trh(s.cfg.Language, "dns.server", index, server.Address)
	if server.Object {
		body += trh(s.cfg.Language, "dns.server.advanced")
	}
	if view.ScriptConfigured {
		body += trh(s.cfg.Language, "dns.script")
	}
	var rows [][]tgButton
	if s.cfg.AllowMutations {
		buttons := []tgButton{newButton(tr(s.cfg.Language, "dns.edit"), dnsCallback("edit", index, etag))}
		if !view.ScriptConfigured {
			buttons = append(buttons, newButton(tr(s.cfg.Language, "dns.delete"), dnsCallback("delete", index, etag)))
		}
		rows = append(rows, buttons)
		if !view.ScriptConfigured {
			rows = append(rows, newRow(
				newButton(tr(s.cfg.Language, "dns.up"), dnsCallback("up", index, etag)),
				newButton(tr(s.cfg.Language, "dns.down"), dnsCallback("down", index, etag))))
		}
	}
	offset := ((index - 1) / routingPageSize) * routingPageSize
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.back"), fmt.Sprintf("dn:page:%d", offset)), newButton(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) offerDNSChange(chatID, adminID int64, msgID int, change dnsChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), dnsBackMarkup(s.cfg.Language))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var view dnsSummary
	if err == nil {
		view, err = currentDNSView(root, change.ETag)
	}
	if err == nil {
		_, err = applyDNSChange(root, change)
	}
	if errors.Is(err, errDNSChanged) {
		s.sendDNSPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
		return
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), dnsBackMarkup(s.cfg.Language))
		return
	}
	var body htmlText
	switch change.Kind {
	case "add":
		body = trh(s.cfg.Language, "dns.confirm.add", safeDNSAddress(change.Value))
	case "update":
		body = trh(s.cfg.Language, "dns.confirm.update", change.Index, view.Servers[change.Index-1].Address, safeDNSAddress(change.Value))
	case "delete":
		body = trh(s.cfg.Language, "dns.confirm.delete", change.Index, view.Servers[change.Index-1].Address)
	case "move":
		direction := tr(s.cfg.Language, "dns.up")
		if change.Value == "down" {
			direction = tr(s.cfg.Language, "dns.down")
		}
		body = trh(s.cfg.Language, "dns.confirm.move", change.Index, direction)
	case "strategy":
		value := change.Value
		if value == "" {
			value = tr(s.cfg.Language, "node.default")
		}
		body = trh(s.cfg.Language, "dns.confirm.strategy", value)
	case "flag":
		var code string
		for key, field := range dnsFlagCodes {
			if field == change.Field {
				code = key
				break
			}
		}
		body = trh(s.cfg.Language, "dns.confirm.flag", dnsSwitchLabel(s.cfg.Language, code, change.Switch))
	}
	if view.ScriptConfigured {
		body += trh(s.cfg.Language, "dns.script")
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{DNS: &change}, body)
}

func (s *BotService) handleDNSInput(msg *tgMessage) bool {
	if msg.Chat == nil || msg.From == nil {
		return false
	}
	state, ok := getDNSWizard(msg.Chat.ID, msg.From.ID)
	if !ok {
		return false
	}
	if strings.HasPrefix(state.Kind, "host-") {
		return s.handleDNSHostInput(msg, state)
	}
	if msg.Command() == "cancel" {
		clearDNSWizard(msg.Chat.ID)
		s.sendDNSPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "wizard.cancelled"))
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
	change := dnsChange{Kind: state.Kind, ETag: state.ETag, Index: state.Index, Value: strings.TrimSpace(msg.Text)}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyDNSChange(root, change)
	}
	if errors.Is(err, errDNSChanged) {
		clearDNSWizard(msg.Chat.ID)
		s.sendDNSPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "dns.changed"))
		return true
	}
	if err != nil {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	clearDNSWizard(msg.Chat.ID)
	s.offerDNSChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func (s *BotService) handleDNSCallback(cb *tgCallback) {
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
			s.sendDNSPage(chatID, msgID, offset, "")
		}
		return
	}
	var etag string
	if len(parts) == 3 {
		etag = parts[2]
	} else if len(parts) == 4 {
		etag = parts[3]
	} else {
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendDNSPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
		return
	}
	view, err := currentDNSView(root, etag)
	if err != nil {
		s.sendDNSPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
		return
	}
	if parts[1] == "v" && len(parts) == 4 {
		index, err := strconv.Atoi(parts[2])
		if err == nil {
			clearInputSessions(chatID)
			s.sendDNSServerDetail(chatID, msgID, index, etag)
		}
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), dnsBackMarkup(s.cfg.Language))
		return
	}
	switch parts[1] {
	case "add", "edit":
		index, kind := 0, "add"
		if parts[1] == "edit" {
			if len(parts) != 4 {
				return
			}
			index, err = strconv.Atoi(parts[2])
			if err != nil || index < 1 || index > len(view.Servers) {
				return
			}
			kind = "update"
		} else if len(parts) != 3 {
			return
		}
		clearInputSessions(chatID)
		setDNSWizard(chatID, dnsWizard{AdminID: cb.From.ID, Kind: kind, Index: index, ETag: etag, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "dns.address_prompt"), dnsBackMarkup(s.cfg.Language))
	case "delete":
		if len(parts) != 4 {
			return
		}
		index, err := strconv.Atoi(parts[2])
		if err != nil {
			return
		}
		s.offerDNSChange(chatID, cb.From.ID, msgID, dnsChange{Kind: "delete", ETag: etag, Index: index})
	case "up", "down":
		if len(parts) != 4 {
			return
		}
		index, err := strconv.Atoi(parts[2])
		if err != nil {
			return
		}
		s.offerDNSChange(chatID, cb.From.ID, msgID, dnsChange{Kind: "move", ETag: etag, Index: index, Value: parts[1]})
	case "strategy":
		if len(parts) != 3 {
			return
		}
		var rows [][]tgButton
		for _, pair := range [][]string{{"UseIP", "UseIPv4"}, {"UseIPv6", "UseSystem"}, {"default"}} {
			var buttons []tgButton
			for _, value := range pair {
				label := value
				if value == "default" {
					label = tr(s.cfg.Language, "node.default")
				}
				buttons = append(buttons, newButton(label, "dn:s:"+value+":"+etag))
			}
			rows = append(rows, buttons)
		}
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "dns.back"), "dn:page:0")))
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "dns.strategy_prompt"), newKeyboard(rows...))
	case "s":
		if len(parts) != 4 {
			return
		}
		value := parts[2]
		if value == "default" {
			value = ""
		}
		s.offerDNSChange(chatID, cb.From.ID, msgID, dnsChange{Kind: "strategy", ETag: etag, Value: value})
	case "f":
		if len(parts) != 4 {
			return
		}
		field := dnsFlagCodes[parts[2]]
		if field == "" {
			return
		}
		s.offerDNSChange(chatID, cb.From.ID, msgID, dnsChange{Kind: "flag", ETag: etag, Field: field, Switch: !view.Flags[field]})
	}
}
