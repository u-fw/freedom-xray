package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type nativeInboundExport struct {
	ChatID  int64
	AdminID int64
	Index   int
	ETag    string
	Expires time.Time
}

var pendingNativeInboundExports = struct {
	sync.Mutex
	items map[string]nativeInboundExport
}{items: map[string]nativeInboundExport{}}

func (s *BotService) askNativeInboundExport(cb *tgbotapi.CallbackQuery, index int, etag string) {
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	if !s.cfg.AllowSensitiveBackup {
		s.sendAlert(chatID, tr(s.cfg.Language, "backup.disabled"))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = lookupNativeInbound(root, index, etag)
	}
	if err != nil {
		s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
		return
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "node.tokenerror"))
		return
	}
	key := hex.EncodeToString(token[:])
	pendingNativeInboundExports.Lock()
	for k, pending := range pendingNativeInboundExports.items {
		if time.Now().After(pending.Expires) {
			delete(pendingNativeInboundExports.items, k)
		}
	}
	pendingNativeInboundExports.items[key] = nativeInboundExport{ChatID: chatID, AdminID: cb.From.ID, Index: index, ETag: etag, Expires: time.Now().Add(3 * time.Minute)}
	pendingNativeInboundExports.Unlock()
	markup := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.confirm"), "ni:exconfirm:"+key),
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.cancel"), "ni:excancel:"+key)))
	s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "inbound.native.export_confirm", index), markup)
}

func (s *BotService) resolveNativeInboundExport(cb *tgbotapi.CallbackQuery, key string, confirm bool) {
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	pendingNativeInboundExports.Lock()
	pending, ok := pendingNativeInboundExports.items[key]
	if ok && pending.ChatID == chatID && pending.AdminID == cb.From.ID {
		delete(pendingNativeInboundExports.items, key)
	}
	pendingNativeInboundExports.Unlock()
	if ok && (pending.ChatID != chatID || pending.AdminID != cb.From.ID) {
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || time.Now().After(pending.Expires) {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.expired"), nativeInboundBackMarkup(s.cfg.Language))
		return
	}
	if !confirm {
		s.sendNativeInboundDetail(chatID, msgID, pending.Index, pending.ETag)
		return
	}
	if !s.cfg.AllowSensitiveBackup {
		s.sendAlert(chatID, tr(s.cfg.Language, "backup.disabled"))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var inbound map[string]any
	if err == nil {
		inbound, err = lookupNativeInbound(root, pending.Index, pending.ETag)
	}
	if err != nil {
		s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
		return
	}
	data, err := json.MarshalIndent(inbound, "", "  ")
	if err != nil || len(data)+1 > telegramJSONDocumentLimit {
		s.sendAlert(chatID, tr(s.cfg.Language, "inbound.native.export_failed"))
		return
	}
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: "xray-inbound.json", Bytes: append(data, '\n')})
	doc.Caption = tr(s.cfg.Language, "inbound.native.export_caption")
	if _, err := s.bot.Send(doc); err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "inbound.native.export_failed"))
		return
	}
	s.sendNativeInboundDetail(chatID, msgID, pending.Index, pending.ETag)
}
