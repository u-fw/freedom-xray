package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type nativeInboundWizard struct {
	AdminID     int64
	Kind        string
	Index       int
	ClientIndex int
	ETag        string
	ListETag    string
	Expires     time.Time
}

var nativeInboundWizards = struct {
	sync.Mutex
	items map[int64]nativeInboundWizard
}{items: map[int64]nativeInboundWizard{}}

func clearNativeInboundWizard(chatID int64) {
	nativeInboundWizards.Lock()
	delete(nativeInboundWizards.items, chatID)
	nativeInboundWizards.Unlock()
}

func setNativeInboundWizard(chatID int64, state nativeInboundWizard) {
	nativeInboundWizards.Lock()
	nativeInboundWizards.items[chatID] = state
	nativeInboundWizards.Unlock()
}

func getNativeInboundWizard(chatID, adminID int64) (nativeInboundWizard, bool) {
	nativeInboundWizards.Lock()
	defer nativeInboundWizards.Unlock()
	state, ok := nativeInboundWizards.items[chatID]
	if ok && time.Now().After(state.Expires) {
		delete(nativeInboundWizards.items, chatID)
		return nativeInboundWizard{}, false
	}
	return state, ok && state.AdminID == adminID
}

func nativeInboundCallback(action string, index int, etag string) string {
	return fmt.Sprintf("ni:%s:%d:%s", action, index, etag)
}

func nativeInboundBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(
		newButton(tr(lang, "inbound.native.back"), "ni:page:0"),
		newButton(tr(lang, "node.backlist"), "menu_nodes")))
}

func (s *BotService) sendNativeInboundPage(chatID int64, msgID, offset int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "nodes.failure", err.Error()), s.backToMainMarkup())
		return
	}
	records, err := nativeInboundRecords(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "nodes.failure", err.Error()), s.backToMainMarkup())
		return
	}
	listETag, err := nativeInboundListETag(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "nodes.failure", err.Error()), s.backToMainMarkup())
		return
	}
	if offset < 0 || len(records) == 0 {
		offset = 0
	}
	if len(records) > 0 && offset >= len(records) {
		offset = ((len(records) - 1) / nodesPerPage) * nodesPerPage
	}
	end := offset + nodesPerPage
	if end > len(records) {
		end = len(records)
	}
	body := trh(s.cfg.Language, "inbound.native.list", len(records))
	if notice != "" {
		body += "\n" + esc(notice) + "\n"
	}
	var rows [][]tgButton
	for _, record := range records[offset:end] {
		name := record.Tag
		if name == "" {
			name = tr(s.cfg.Language, "inbound.native.untagged")
		}
		label := fmt.Sprintf("%d. %s · %s", record.Index, name, record.Protocol)
		rows = append(rows, newRow(newButton(truncateLabel(label, 64), nativeInboundCallback("v", record.Index, record.ETag))))
	}
	var pages []tgButton
	if offset > 0 {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("ni:page:%d", offset-nodesPerPage)))
	}
	if end < len(records) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), fmt.Sprintf("ni:page:%d", end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.native.add"), "ni:add:"+listETag)))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("ni:page:%d", offset)), newButton(tr(s.cfg.Language, "node.backlist"), "menu_nodes")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendNativeInboundDetail(chatID int64, msgID, index int, etag string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = lookupNativeInbound(root, index, etag)
	}
	if err != nil {
		s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
		return
	}
	records, err := nativeInboundRecords(root)
	if err != nil || index > len(records) {
		s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
		return
	}
	record := records[index-1]
	name, listen, port := record.Tag, record.Listen, record.Port
	if name == "" {
		name = tr(s.cfg.Language, "inbound.native.untagged")
	}
	if listen == "" {
		listen = tr(s.cfg.Language, "inbound.native.all_interfaces")
	}
	if port == "" {
		port = tr(s.cfg.Language, "node.notconfigured")
	}
	body := trh(s.cfg.Language, "inbound.native.detail", index, name, record.Protocol, listen, port)
	if record.ClientCount != nil {
		body += trh(s.cfg.Language, "inbound.native.clients", *record.ClientCount)
	}
	var rows [][]tgButton
	switch {
	case record.Protected:
		body += trh(s.cfg.Language, "inbound.native.protected")
	case record.Managed:
		body += trh(s.cfg.Language, "inbound.native.vless")
		if record.Tag != "" && record.Tag != "<hidden>" {
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.native.open_managed"), "node_detail:"+userCallbackKey(record.Tag))))
		}
	default:
		body += trh(s.cfg.Language, "inbound.native.unmanaged")
		if record.Protocol == "vmess" || record.Protocol == "trojan" {
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.client.open"), nativeClientCallback("list", index, 0, etag))))
		}
		if s.cfg.AllowMutations {
			if !strings.EqualFold(record.Protocol, "tun") {
				rows = append(rows, newRow(
					newButton(tr(s.cfg.Language, "inbound.native.port"), nativeInboundCallback("port", index, etag)),
					newButton(tr(s.cfg.Language, "inbound.native.listen"), nativeInboundCallback("listen", index, etag))))
			}
			rows = append(rows, newRow(
				newButton(tr(s.cfg.Language, "inbound.native.replace"), nativeInboundCallback("replace", index, etag)),
				newButton(tr(s.cfg.Language, "inbound.native.delete"), nativeInboundCallback("delete", index, etag))))
		}
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.native.export"), nativeInboundCallback("export", index, etag))))
	offset := ((index - 1) / nodesPerPage) * nodesPerPage
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "inbound.native.back"), fmt.Sprintf("ni:page:%d", offset)), newButton(tr(s.cfg.Language, "node.backlist"), "menu_nodes")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) offerNativeInboundChange(chatID, adminID int64, msgID int, change nativeInboundChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), nativeInboundBackMarkup(s.cfg.Language))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var record nativeInboundRecord
	if err == nil && change.Kind != "add" {
		var records []nativeInboundRecord
		records, err = nativeInboundRecords(root)
		if err == nil && change.Index >= 1 && change.Index <= len(records) {
			record = records[change.Index-1]
			if record.ETag != change.ETag {
				err = errNativeInboundChanged
			} else if record.Protected || record.Managed {
				err = errors.New("this inbound is protected by the managed node workflow")
			}
		} else if err == nil {
			err = errNativeInboundChanged
		}
	}
	var impact nodeDeletionReport
	if err == nil && change.Kind == "delete" {
		var item map[string]any
		item, err = lookupNativeInbound(root, change.Index, change.ETag)
		if err == nil {
			change.Tag, _ = item["tag"].(string)
			change.ListETag, err = nativeInboundListETag(root)
		}
		if err == nil && change.Tag != "" {
			err = validateNativeInboundAPI(root, s.cfg.StatsAPIServer)
		}
		if err == nil && change.Tag != "" {
			impact, change.ImpactETag, err = inspectNodeDeletionForConfirmation(s.cfg.XrayConfigPath, change.Tag)
			if err == nil {
				fingerprint, e := nodeDeletionFingerprint(root, impact)
				if e != nil || fingerprint != change.ImpactETag {
					err = errNativeInboundChanged
				}
			}
		}
	}
	if err == nil && !(change.Kind == "delete" && change.Tag != "") {
		_, err = applyNativeInboundChange(root, change, s.cfg.StatsAPIServer, s.cfg.XrayConfigPath)
	}
	if errors.Is(err, errNativeInboundChanged) {
		s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
		return
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), nativeInboundBackMarkup(s.cfg.Language))
		return
	}
	var body htmlText
	switch change.Kind {
	case "add":
		protocol, _ := change.Inbound["protocol"].(string)
		body = trh(s.cfg.Language, "inbound.native.confirm.add", safeInboundField(protocol))
	case "replace":
		body = trh(s.cfg.Language, "inbound.native.confirm.replace", change.Index, record.Tag, record.Protocol)
	case "port", "listen":
		value := change.Value
		if value == "" {
			value = tr(s.cfg.Language, "inbound.native.all_interfaces")
		}
		body = trh(s.cfg.Language, "inbound.native.confirm.field", change.Index, tr(s.cfg.Language, "inbound.native."+change.Kind), value)
	case "delete":
		body = trh(s.cfg.Language, "inbound.native.confirm.delete", change.Index, record.Tag, record.Protocol)
		if len(impact.RoutingRules) > 0 {
			parts := make([]string, 0, len(impact.RoutingRules))
			for _, index := range impact.RoutingRules {
				parts = append(parts, strconv.Itoa(index+1))
			}
			body += trh(s.cfg.Language, "node.delete.routing", strings.Join(parts, ", "))
		}
		if len(impact.SuspendedUsers) > 0 {
			body += trh(s.cfg.Language, "node.delete.suspended", strings.Join(impact.SuspendedUsers, ", "))
		}
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{NativeInbound: &change}, body)
}

func (s *BotService) handleNativeInboundInput(msg *tgMessage) bool {
	if msg.Chat == nil || msg.From == nil {
		return false
	}
	state, ok := getNativeInboundWizard(msg.Chat.ID, msg.From.ID)
	if !ok {
		return false
	}
	if strings.HasPrefix(state.Kind, "client.") {
		return s.handleNativeClientInput(msg, state)
	}
	if msg.Command() == "cancel" {
		clearNativeInboundWizard(msg.Chat.ID)
		s.sendNativeInboundPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "wizard.cancelled"))
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
	change := nativeInboundChange{Kind: state.Kind, Index: state.Index, ETag: state.ETag, ListETag: state.ListETag, Value: strings.TrimSpace(msg.Text)}
	if state.Kind == "add" || state.Kind == "replace" {
		input := []byte(msg.Text)
		if msg.Document != nil {
			var err error
			input, err = readTelegramJSONDocument(s.tg, msg.Document)
			if err != nil {
				s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
				return true
			}
		}
		if len(input) > telegramJSONDocumentLimit {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", "JSON document exceeds 1 MiB"))
			return true
		}
		value, err := decodeNetworkObject(input)
		if err != nil {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
			return true
		}
		change.Inbound, change.Value = value, ""
	} else if state.Kind == "listen" && change.Value == "-" {
		change.Value = ""
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyNativeInboundChange(root, change, s.cfg.StatsAPIServer, s.cfg.XrayConfigPath)
	}
	if errors.Is(err, errNativeInboundChanged) {
		clearNativeInboundWizard(msg.Chat.ID)
		s.sendNativeInboundPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "inbound.native.changed"))
		return true
	}
	if err != nil {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	clearNativeInboundWizard(msg.Chat.ID)
	s.offerNativeInboundChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func (s *BotService) handleNativeInboundCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	parts := strings.Split(cb.Data, ":")
	if len(parts) == 3 && (parts[1] == "exconfirm" || parts[1] == "excancel") {
		s.resolveNativeConfigExport(cb, parts[2], parts[1] == "exconfirm")
		return
	}
	if len(parts) == 3 && parts[1] == "page" {
		offset, err := strconv.Atoi(parts[2])
		if err == nil {
			clearInputSessions(chatID)
			s.sendNativeInboundPage(chatID, msgID, offset, "")
		}
		return
	}
	if len(parts) == 3 && parts[1] == "add" {
		if !s.cfg.AllowMutations {
			return
		}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		var current string
		if err == nil {
			current, err = nativeInboundListETag(root)
		}
		if err != nil || len(parts[2]) != 24 || current != parts[2] {
			s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
			return
		}
		clearInputSessions(chatID)
		setNativeInboundWizard(chatID, nativeInboundWizard{AdminID: cb.From.ID, Kind: "add", ListETag: current, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "inbound.native.json_prompt"), nativeInboundBackMarkup(s.cfg.Language))
		return
	}
	if len(parts) != 4 {
		return
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil {
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = lookupNativeInbound(root, index, parts[3])
	}
	if err != nil {
		s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
		return
	}
	if parts[1] == "v" {
		clearInputSessions(chatID)
		s.sendNativeInboundDetail(chatID, msgID, index, parts[3])
		return
	}
	if parts[1] == "export" {
		clearInputSessions(chatID)
		s.askNativeConfigExport(cb, "inbound", index, parts[3])
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), nativeInboundBackMarkup(s.cfg.Language))
		return
	}
	records, err := nativeInboundRecords(root)
	if err != nil || index < 1 || index > len(records) || records[index-1].Protected || records[index-1].Managed {
		return
	}
	switch parts[1] {
	case "port", "listen", "replace":
		clearInputSessions(chatID)
		setNativeInboundWizard(chatID, nativeInboundWizard{AdminID: cb.From.ID, Kind: parts[1], Index: index, ETag: parts[3], Expires: time.Now().Add(5 * time.Minute)})
		prompt := "inbound.native." + parts[1] + "_prompt"
		if parts[1] == "replace" {
			prompt = "inbound.native.json_prompt"
		}
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, prompt), nativeInboundBackMarkup(s.cfg.Language))
	case "delete":
		s.offerNativeInboundChange(chatID, cb.From.ID, msgID, nativeInboundChange{Kind: "delete", Index: index, ETag: parts[3]})
	}
}
