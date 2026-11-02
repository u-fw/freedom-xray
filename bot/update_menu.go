package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

var safeUpdateMutex sync.Mutex

// A typed /update is explicit consent to update the management components.
// Buttons can be tapped by accident, so they and every Xray-core change go
// through a separate, time-limited confirmation. Updates run in the background
// so other admins are not blocked while packages download and verify.
func (s *BotService) runSafeUpdates(chatID int64, msgID int, components []string) {
	if !s.cfg.AllowUpdates {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.disabled"), s.backToMainMarkup())
		return
	}
	if !safeUpdateMutex.TryLock() {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.busy"), s.backToMainMarkup())
		return
	}
	s.deliverLater(chatID, msgID, trh(s.cfg.Language, "update.running"), func() (htmlText, tgKeyboard) {
		defer safeUpdateMutex.Unlock()
		return s.safeUpdateResults(components)
	})
}

// safeUpdateResults updates the manager script and Bot and describes the outcome.
func (s *BotService) safeUpdateResults(components []string) (htmlText, tgKeyboard) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	results := make([]string, 0, len(components))
	for _, comp := range components {
		if !s.cfg.AllowUpdates {
			results = append(results, tr(s.cfg.Language, "update.cancelled.disabled"))
			break
		}
		switch comp {
		case "script":
			commit, err := remoteComponentVersion(ctx, "script")
			if err != nil {
				results = append(results, tr(s.cfg.Language, "update.script.fetch", err.Error()))
				continue
			}
			message, err := checkedScriptUpdate(ctx, commit)
			if err != nil {
				results = append(results, tr(s.cfg.Language, "update.script.failed", err.Error()))
			} else {
				results = append(results, tr(s.cfg.Language, "update.script.ok", message))
			}
		case "bot":
			// checkedBotUpdate validates the release checksum and schedules an
			// independent watchdog. Its return does NOT mean the reboot passed.
			message, err := checkedBotUpdate(ctx)
			if err != nil {
				results = append(results, tr(s.cfg.Language, "update.bot.failed", err.Error()))
			} else {
				results = append(results, tr(s.cfg.Language, "update.bot.ok", message))
			}
		default:
			results = append(results, tr(s.cfg.Language, "update.refuse", comp))
		}
	}
	rows := newKeyboard(newRow(
		newButton(tr(s.cfg.Language, "update.opencenter"), "menu_update_center"),
		newButton(tr(s.cfg.Language, "menu.home"), "menu_main")))
	return trh(s.cfg.Language, "update.result", strings.Join(results, "\n\n")), rows
}

type pendingUpdate struct {
	Component string
	Version   string
	AdminID   int64
	ChatID    int64
	Deadline  time.Time
}

var pendingUpdates = struct {
	sync.Mutex
	items map[string]pendingUpdate
}{items: map[string]pendingUpdate{}}

func (s *BotService) showUpdatesMenu(chatID int64, msgID int) {
	if !s.cfg.AllowUpdates {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.disabled.title"), s.backToMainMarkup())
		return
	}
	s.renderLater(chatID, msgID, trh(s.cfg.Language, "update.checking"), s.updateCenterScreen)
}

func (s *BotService) updateCenterScreen() (htmlText, tgKeyboard) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out htmlBuilder
	out.Add(trh(s.cfg.Language, "update.center.title"))
	rows := [][]tgButton{}
	for _, comp := range []string{"script", "bot", "core"} {
		if comp == "core" && !s.cfg.AllowCoreUpdates {
			out.Add(trh(s.cfg.Language, "update.core.disabled"))
			continue
		}
		remote, err := remoteComponentVersion(ctx, comp)
		label := componentLabel(comp)
		current := currentComponentVersion(comp, s.cfg.XrayBinPath)
		if err != nil {
			out.Add(trh(s.cfg.Language, "update.fetchfailed", label, err.Error()))
			continue
		}
		if comp == "script" && len(remote) > 12 {
			out.Add(trh(s.cfg.Language, "update.row.commit", label, current, remote[:12]))
		} else {
			out.Add(trh(s.cfg.Language, "update.row.upstream", label, current, remote))
		}
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "update.update.now", label), "update_ask:"+comp)))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "update.update.both"), "update_all")))
	if s.cfg.AllowCoreUpdates {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "update.rollback"), "update_ask:core-rollback")))
	}
	out.Add(trh(s.cfg.Language, "update.note"))
	rows = append(rows, toolsNavRow(s.cfg.Language))
	return out.HTML(), newKeyboard(rows...)
}

func componentLabel(comp string) string {
	switch comp {
	case "script":
		return "freedom.sh"
	case "bot":
		return "Telegram Bot"
	case "core":
		return "Xray-core"
	case "core-rollback":
		return "Xray-core rollback"
	}
	return comp
}

func (s *BotService) askUpdate(cb *tgCallback, comp string) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	if !s.cfg.AllowUpdates || ((comp == "core" || comp == "core-rollback") && !s.cfg.AllowCoreUpdates) {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.unauthorized"), s.backToMainMarkup())
		return
	}
	if comp != "script" && comp != "bot" && comp != "core" && comp != "core-rollback" {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.unsupported"), s.backToMainMarkup())
		return
	}
	adminID := cb.From.ID
	s.renderLater(chatID, msgID, trh(s.cfg.Language, "update.checking"), func() (htmlText, tgKeyboard) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		version := "previous"
		var err error
		if comp != "core-rollback" {
			version, err = remoteComponentVersion(ctx, comp)
		}
		if err != nil {
			return trh(s.cfg.Language, "update.fetcherror", err.Error()), s.backToMainMarkup()
		}
		note := ""
		if comp == "core" || comp == "core-rollback" {
			note = tr(s.cfg.Language, "update.corenote")
		}
		body := trh(s.cfg.Language, "update.confirmbody", componentLabel(comp), version, currentComponentVersion(comp, s.cfg.XrayBinPath), note)
		return s.updateConfirmation(chatID, adminID, pendingUpdate{Component: comp, Version: version}, body)
	})
}

// askUpdateAll confirms a button request to update both management components.
func (s *BotService) askUpdateAll(chatID, adminID int64, msgID int) {
	if !s.cfg.AllowUpdates {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.unauthorized"), s.backToMainMarkup())
		return
	}
	body, markup := s.updateConfirmation(chatID, adminID, pendingUpdate{Component: "all"}, trh(s.cfg.Language, "update.confirm.all"))
	s.sendOrEdit(chatID, msgID, body, markup)
}

// updateConfirmation stores a time-limited update request and returns its
// confirmation screen.
func (s *BotService) updateConfirmation(chatID, adminID int64, pending pendingUpdate, body htmlText) (htmlText, tgKeyboard) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return trh(s.cfg.Language, "update.tokenfailed"), s.backToMainMarkup()
	}
	token := hex.EncodeToString(nonce[:])
	pending.ChatID, pending.AdminID, pending.Deadline = chatID, adminID, time.Now().Add(3*time.Minute)
	pendingUpdates.Lock()
	for id, p := range pendingUpdates.items {
		if time.Now().After(p.Deadline) {
			delete(pendingUpdates.items, id)
		}
	}
	pendingUpdates.items[token] = pending
	pendingUpdates.Unlock()
	return body, newKeyboard(newRow(
		newButton(tr(s.cfg.Language, "update.confirm"), "update_confirm:"+token),
		newButton(tr(s.cfg.Language, "common.cancel"), "update_cancel:"+token),
	))
}

func (s *BotService) resolveUpdate(cb *tgCallback, confirm bool) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	token := strings.TrimPrefix(strings.TrimPrefix(cb.Data, "update_confirm:"), "update_cancel:")
	pendingUpdates.Lock()
	pending, ok := pendingUpdates.items[token]
	if ok && pending.ChatID == chatID && pending.AdminID == cb.From.ID {
		delete(pendingUpdates.items, token)
	}
	pendingUpdates.Unlock()
	if ok && (pending.AdminID != cb.From.ID || pending.ChatID != chatID) {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || time.Now().After(pending.Deadline) {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.expired"), s.backToMainMarkup())
		return
	}
	if !confirm {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.cancelled"), s.backToMainMarkup())
		return
	}
	if !s.cfg.AllowUpdates || ((pending.Component == "core" || pending.Component == "core-rollback") && !s.cfg.AllowCoreUpdates) {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.permissions"), s.backToMainMarkup())
		return
	}
	if pending.Component == "all" {
		s.runSafeUpdates(chatID, msgID, []string{"script", "bot"})
		return
	}
	managed := pending.Component == "script" || pending.Component == "bot"
	if managed && !safeUpdateMutex.TryLock() {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "update.busy"), s.backToMainMarkup())
		return
	}
	s.deliverLater(chatID, msgID, trh(s.cfg.Language, "update.verifying"), func() (htmlText, tgKeyboard) {
		if managed {
			defer safeUpdateMutex.Unlock()
		}
		return s.applyConfirmedUpdate(pending)
	})
}

// applyConfirmedUpdate installs the confirmed component version.
func (s *BotService) applyConfirmedUpdate(pending pendingUpdate) (htmlText, tgKeyboard) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Check a mutable upstream release label again before downloading.
	if pending.Component == "core" || pending.Component == "bot" {
		latest, err := remoteComponentVersion(ctx, pending.Component)
		if err != nil {
			return trh(s.cfg.Language, "update.verifyfailed", err.Error()), s.backToMainMarkup()
		}
		if latest != pending.Version {
			return trh(s.cfg.Language, "update.changed"), s.backToMainMarkup()
		}
	}
	var result string
	var err error
	switch pending.Component {
	case "script":
		result, err = checkedScriptUpdate(ctx, pending.Version)
	case "bot":
		result, err = checkedBotUpdate(ctx)
	case "core":
		result, err = checkedCoreUpdate(ctx, s.cfg.XrayBinPath, s.cfg.XrayConfigPath, s.cfg.StatsAPIServer, pending.Version)
	case "core-rollback":
		result, err = checkedCoreRollback(s.cfg.XrayBinPath, s.cfg.XrayConfigPath, s.cfg.StatsAPIServer)
	default:
		err = fmt.Errorf("unknown update type")
	}
	if err != nil {
		return trh(s.cfg.Language, "update.failed", err.Error()), s.backToMainMarkup()
	}
	return trh(s.cfg.Language, "update.resultprefix", result), s.backToMainMarkup()
}
