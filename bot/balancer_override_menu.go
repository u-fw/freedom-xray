package main

import (
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Short action names keep the tag hash, state reference and target hash within
// Telegram's 64-byte callback limit (the longest selection is exactly 64).
func balancerOverrideCallback(action, tag, state, value string) string {
	return "bl:" + action + ":" + userCallbackKey(tag) + ":" + state + ":" + value
}

func (s *BotService) returnBalancerLiveLatest(chatID int64, msgID int, tag, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		record, err := findBalancerByKey(root, userCallbackKey(tag))
		if err == nil {
			s.sendBalancerLiveNotice(chatID, msgID, record, notice)
			return
		}
	}
	s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
}

func (s *BotService) sendBalancerOverridePicker(chatID int64, msgID int, record balancerRecord, status balancerLiveStatus, offset int) {
	targets := status.OverrideTargets
	if offset < 0 || len(targets) == 0 {
		offset = 0
	}
	if len(targets) > 0 && offset >= len(targets) {
		offset = ((len(targets) - 1) / routingPageSize) * routingPageSize
	}
	end := offset + routingPageSize
	if end > len(targets) {
		end = len(targets)
	}
	current := status.Override
	if current == "" {
		current = tr(s.cfg.Language, "balancer.override.auto")
	}
	body := tr(s.cfg.Language, "balancer.override.picker", mdEsc(record.Tag), mdEsc(current))
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, target := range targets[offset:end] {
		if target == status.Override {
			continue
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(truncateLabel(target, 50), balancerOverrideCallback("op", record.Tag, status.StateETag, userCallbackKey(target)))))
	}
	var pages []tgbotapi.InlineKeyboardButton
	if offset > 0 {
		pages = append(pages, tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.prev"), balancerOverrideCallback("ol", record.Tag, status.StateETag, strconv.Itoa(offset-routingPageSize))))
	}
	if end < len(targets) {
		pages = append(pages, tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.next"), balancerOverrideCallback("ol", record.Tag, status.StateETag, strconv.Itoa(end))))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	if status.Override != "" {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "balancer.override.restore"), balancerOverrideCallback("op", record.Tag, status.StateETag, "auto"))))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.back"), balancerCallback("live", record.Tag, record.ETag))))
	s.sendOrEdit(chatID, msgID, body, tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) handleBalancerOverrideCallback(cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	parts := strings.Split(cb.Data, ":")
	if len(parts) != 5 || len(parts[3]) != 24 {
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), balancerBackMarkup(s.cfg.Language))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var record balancerRecord
	if err == nil {
		record, err = findBalancerByKey(root, parts[2])
	}
	if err != nil {
		s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
		return
	}
	status, err := queryBalancerLiveFromRoot(s.cfg, root, record.Tag)
	if err != nil {
		s.sendBalancerLiveNotice(chatID, msgID, record, err.Error())
		return
	}
	if status.StateETag != parts[3] {
		s.sendBalancerLiveNotice(chatID, msgID, record, tr(s.cfg.Language, "balancer.override.changed"))
		return
	}
	if parts[1] == "ol" {
		offset, err := strconv.Atoi(parts[4])
		if err == nil {
			s.sendBalancerOverridePicker(chatID, msgID, record, status, offset)
		}
		return
	}
	if parts[1] != "op" {
		return
	}
	target := ""
	if parts[4] != "auto" {
		count := 0
		for _, candidate := range status.OverrideTargets {
			if userCallbackKey(candidate) == parts[4] {
				target = candidate
				count++
			}
		}
		if count != 1 {
			s.sendBalancerLiveNotice(chatID, msgID, record, tr(s.cfg.Language, "balancer.override.changed"))
			return
		}
	}
	if target == status.Override {
		s.sendBalancerLive(chatID, msgID, record)
		return
	}
	label := target
	if label == "" {
		label = tr(s.cfg.Language, "balancer.override.auto")
	}
	change := &balancerOverrideChange{Tag: record.Tag, StateETag: status.StateETag, Target: target}
	body := tr(s.cfg.Language, "balancer.override.confirm", mdEsc(record.Tag), mdEsc(label))
	s.queueGuidedRoutingChange(chatID, cb.From.ID, msgID, pendingRoutingChange{Override: change}, body)
}

func balancerOverrideFailure(lang string, err error) string {
	return fmt.Sprintf("%s: %s", tr(lang, "balancer.override.failed"), err.Error())
}
