package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// All service controls share the configuration lock with config transactions.
func manageXrayService(cfg *BotConfig, action string) error {
	if action != "start" && action != "stop" && action != "restart" {
		return errors.New("unsupported service action")
	}
	lock, err := acquireConfigLock(cfg.XrayConfigPath, 20*time.Second)
	if err != nil {
		return err
	}
	defer releaseConfigLock(lock)
	if action != "start" {
		snapshotTrafficBestEffortLocked(cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "systemctl", action, "xray").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s failed: %w: %s", action, err, strings.TrimSpace(string(output)))
	}
	output, err := exec.CommandContext(ctx, "systemctl", "show", "xray", "--property=ActiveState", "--value").Output()
	if err != nil {
		return fmt.Errorf("cannot verify Xray service state: %w", err)
	}
	want := "active"
	if action == "stop" {
		want = "inactive"
	}
	if strings.TrimSpace(string(output)) != want {
		return fmt.Errorf("Xray service state is %q, expected %s", strings.TrimSpace(string(output)), want)
	}
	return nil
}

type pendingServiceAction struct {
	chatID, adminID int64
	action          string
	expires         time.Time
}

var pendingServiceActions = struct {
	sync.Mutex
	items map[string]pendingServiceAction
}{items: map[string]pendingServiceAction{}}

func (s *BotService) askServiceAction(cb *tgbotapi.CallbackQuery, action string) {
	if cb.Message == nil || cb.From == nil || (action != "start" && action != "stop" && action != "collector") {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "node.tokenerror"))
		return
	}
	key := hex.EncodeToString(token[:])
	pendingServiceActions.Lock()
	for k, v := range pendingServiceActions.items {
		if time.Now().After(v.expires) {
			delete(pendingServiceActions.items, k)
		}
	}
	pendingServiceActions.items[key] = pendingServiceAction{chatID: chatID, adminID: cb.From.ID, action: action, expires: time.Now().Add(3 * time.Minute)}
	pendingServiceActions.Unlock()
	markup := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.confirm"), "service_confirm:"+key),
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.cancel"), "menu_status")))
	s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "service.ask."+action), markup)
}

func (s *BotService) resolveServiceAction(cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	key := strings.TrimPrefix(cb.Data, "service_confirm:")
	pendingServiceActions.Lock()
	pending, ok := pendingServiceActions.items[key]
	if ok && pending.chatID == chatID && pending.adminID == cb.From.ID {
		delete(pendingServiceActions.items, key)
	}
	pendingServiceActions.Unlock()
	if !ok || pending.chatID != chatID || pending.adminID != cb.From.ID || time.Now().After(pending.expires) {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.expired"), s.backToMainMarkup())
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	var err error
	if pending.action == "collector" {
		if s.cfg.ConfigFile == "" {
			err = errors.New("Bot configuration path is unavailable")
		} else {
			err = ensureTrafficCollectorUnit(s.cfg.ConfigFile, s.cfg)
		}
	} else {
		err = manageXrayService(s.cfg, pending.action)
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "service.failed", mdEsc(err.Error())), s.backToMainMarkup())
		return
	}
	s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "service.done."+pending.action), s.backToMainMarkup())
}
