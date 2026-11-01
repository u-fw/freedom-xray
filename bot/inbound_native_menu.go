package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

func nativeInboundBackMarkup(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr(lang, "inbound.native.back"), "ni:page:0"),
		tgbotapi.NewInlineKeyboardButtonData(tr(lang, "node.backlist"), "menu_nodes")))
}

func (s *BotService) sendNativeInboundPage(chatID int64, msgID, offset int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "nodes.failure", mdEsc(err.Error())), s.backToMainMarkup())
		return
	}
	records, err := nativeInboundRecords(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "nodes.failure", mdEsc(err.Error())), s.backToMainMarkup())
		return
	}
	listETag, err := nativeInboundListETag(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "nodes.failure", mdEsc(err.Error())), s.backToMainMarkup())
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
	body := tr(s.cfg.Language, "inbound.native.list", len(records))
	if notice != "" {
		body += "\n" + mdEsc(notice) + "\n"
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, record := range records[offset:end] {
		name := record.Tag
		if name == "" {
			name = tr(s.cfg.Language, "inbound.native.untagged")
		}
		label := fmt.Sprintf("%d. %s · %s", record.Index, name, record.Protocol)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(truncateLabel(label, 64), nativeInboundCallback("v", record.Index, record.ETag))))
	}
	var pages []tgbotapi.InlineKeyboardButton
	if offset > 0 {
		pages = append(pages, tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("ni:page:%d", offset-nodesPerPage)))
	}
	if end < len(records) {
		pages = append(pages, tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.next"), fmt.Sprintf("ni:page:%d", end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	if s.cfg.AllowMutations {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.native.add"), "ni:add:"+listETag)))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("ni:page:%d", offset)), tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "node.backlist"), "menu_nodes")))
	s.sendOrEdit(chatID, msgID, body, tgbotapi.NewInlineKeyboardMarkup(rows...))
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
	body := tr(s.cfg.Language, "inbound.native.detail", index, mdEsc(name), mdEsc(record.Protocol), mdEsc(listen), mdEsc(port))
	if record.ClientCount != nil {
		body += tr(s.cfg.Language, "inbound.native.clients", *record.ClientCount)
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	switch {
	case record.Protected:
		body += tr(s.cfg.Language, "inbound.native.protected")
	case record.Managed:
		body += tr(s.cfg.Language, "inbound.native.vless")
		if record.Tag != "" && record.Tag != "<hidden>" {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.native.open_managed"), "node_detail:"+userCallbackKey(record.Tag))))
		}
	default:
		body += tr(s.cfg.Language, "inbound.native.unmanaged")
		if record.Protocol == "vmess" || record.Protocol == "trojan" {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.client.open"), nativeClientCallback("list", index, 0, etag))))
		}
		if s.cfg.AllowMutations {
			if !strings.EqualFold(record.Protocol, "tun") {
				rows = append(rows, tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.native.port"), nativeInboundCallback("port", index, etag)),
					tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.native.listen"), nativeInboundCallback("listen", index, etag))))
			}
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.native.replace"), nativeInboundCallback("replace", index, etag)),
				tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.native.delete"), nativeInboundCallback("delete", index, etag))))
		}
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.native.export"), nativeInboundCallback("export", index, etag))))
	offset := ((index - 1) / nodesPerPage) * nodesPerPage
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "inbound.native.back"), fmt.Sprintf("ni:page:%d", offset)), tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "node.backlist"), "menu_nodes")))
	s.sendOrEdit(chatID, msgID, body, tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) offerNativeInboundChange(chatID, adminID int64, msgID int, change nativeInboundChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), nativeInboundBackMarkup(s.cfg.Language))
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
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "routing.edit.invalid", mdEsc(err.Error())), nativeInboundBackMarkup(s.cfg.Language))
		return
	}
	var body string
	switch change.Kind {
	case "add":
		protocol, _ := change.Inbound["protocol"].(string)
		body = tr(s.cfg.Language, "inbound.native.confirm.add", mdEsc(safeInboundField(protocol)))
	case "replace":
		body = tr(s.cfg.Language, "inbound.native.confirm.replace", change.Index, mdEsc(record.Tag), mdEsc(record.Protocol))
	case "port", "listen":
		value := change.Value
		if value == "" {
			value = tr(s.cfg.Language, "inbound.native.all_interfaces")
		}
		body = tr(s.cfg.Language, "inbound.native.confirm.field", change.Index, mdEsc(tr(s.cfg.Language, "inbound.native."+change.Kind)), mdEsc(value))
	case "delete":
		body = tr(s.cfg.Language, "inbound.native.confirm.delete", change.Index, mdEsc(record.Tag), mdEsc(record.Protocol))
		if len(impact.RoutingRules) > 0 {
			parts := make([]string, 0, len(impact.RoutingRules))
			for _, index := range impact.RoutingRules {
				parts = append(parts, strconv.Itoa(index+1))
			}
			body += tr(s.cfg.Language, "node.delete.routing", mdCode(strings.Join(parts, ", ")))
		}
		if len(impact.SuspendedUsers) > 0 {
			body += tr(s.cfg.Language, "node.delete.suspended", mdCode(strings.Join(impact.SuspendedUsers, ", ")))
		}
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{NativeInbound: &change}, body)
}

func (s *BotService) handleNativeInboundInput(msg *tgbotapi.Message) bool {
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
		s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	change := nativeInboundChange{Kind: state.Kind, Index: state.Index, ETag: state.ETag, ListETag: state.ListETag, Value: strings.TrimSpace(msg.Text)}
	if state.Kind == "add" || state.Kind == "replace" {
		input := []byte(msg.Text)
		if msg.Document != nil {
			var err error
			input, err = readTelegramJSONDocument(s.bot, msg.Document)
			if err != nil {
				s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
				return true
			}
		}
		if len(input) > telegramJSONDocumentLimit {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", "JSON document exceeds 1 MiB"))
			return true
		}
		value, err := decodeNetworkObject(input)
		if err != nil {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
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
		s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	clearNativeInboundWizard(msg.Chat.ID)
	s.offerNativeInboundChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func (s *BotService) handleNativeInboundCallback(cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	parts := strings.Split(cb.Data, ":")
	if len(parts) == 3 && (parts[1] == "exconfirm" || parts[1] == "excancel") {
		s.resolveNativeInboundExport(cb, parts[2], parts[1] == "exconfirm")
		return
	}
	if len(parts) == 3 && parts[1] == "page" {
		offset, err := strconv.Atoi(parts[2])
		if err == nil {
			clearRoutingWizard(chatID)
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
		clearOtherRoutingInputs(chatID)
		setNativeInboundWizard(chatID, nativeInboundWizard{AdminID: cb.From.ID, Kind: "add", ListETag: current, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "inbound.native.json_prompt"), nativeInboundBackMarkup(s.cfg.Language))
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
		clearRoutingWizard(chatID)
		s.sendNativeInboundDetail(chatID, msgID, index, parts[3])
		return
	}
	if parts[1] == "export" {
		clearRoutingWizard(chatID)
		s.askNativeInboundExport(cb, index, parts[3])
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), nativeInboundBackMarkup(s.cfg.Language))
		return
	}
	records, err := nativeInboundRecords(root)
	if err != nil || index < 1 || index > len(records) || records[index-1].Protected || records[index-1].Managed {
		return
	}
	switch parts[1] {
	case "port", "listen", "replace":
		clearOtherRoutingInputs(chatID)
		setNativeInboundWizard(chatID, nativeInboundWizard{AdminID: cb.From.ID, Kind: parts[1], Index: index, ETag: parts[3], Expires: time.Now().Add(5 * time.Minute)})
		prompt := "inbound.native." + parts[1] + "_prompt"
		if parts[1] == "replace" {
			prompt = "inbound.native.json_prompt"
		}
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, prompt), nativeInboundBackMarkup(s.cfg.Language))
	case "delete":
		s.offerNativeInboundChange(chatID, cb.From.ID, msgID, nativeInboundChange{Kind: "delete", Index: index, ETag: parts[3]})
	}
}
