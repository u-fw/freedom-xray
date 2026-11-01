package main

import (
	"errors"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// The wizard survives invalid input. Consuming the exact state after a download
// ensures a cancelled or replaced wizard cannot later queue a confirmation.
func (s *BotService) handleNativeBalancingInput(msg *tgbotapi.Message, state balancerWizard) bool {
	input := []byte(msg.Text)
	if msg.Document != nil {
		var err error
		input, err = readTelegramJSONDocument(s.bot, msg.Document)
		if err != nil {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
			return true
		}
	}
	item, err := decodeNetworkObject(input)
	if err == nil {
		if state.Step == "monitor-native" {
			item, err = normalizeObservatoryObject(item)
		} else {
			item, err = normalizeBalancerObject(item)
		}
	}
	if err != nil {
		s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if state.Step == "monitor-native" {
		change := observatoryChange{Kind: state.Tag, Action: "put", ETag: state.ETag, Config: item}
		if err == nil {
			_, err = applyObservatoryChange(root, change)
		}
		if errors.Is(err, errObservatoryChanged) {
			if consumeBalancerWizard(msg.Chat.ID, state) {
				s.sendObservatoryPage(msg.Chat.ID, 0, tr(s.cfg.Language, "monitor.changed"))
			}
			return true
		}
		if err == nil {
			if consumeBalancerWizard(msg.Chat.ID, state) {
				s.offerObservatoryChange(msg.Chat.ID, msg.From.ID, 0, change)
			}
			return true
		}
	} else {
		tag := state.Tag
		if state.Step == "native-create" {
			tag, _ = item["tag"].(string)
		}
		change := balancerChange{Kind: "put", Tag: tag, ETag: state.ETag, Balancer: item}
		if err == nil {
			_, err = applyBalancerChange(root, change)
		}
		if errors.Is(err, errBalancerChanged) {
			if consumeBalancerWizard(msg.Chat.ID, state) {
				s.sendBalancerPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "balancer.changed"))
			}
			return true
		}
		if err == nil {
			if consumeBalancerWizard(msg.Chat.ID, state) {
				s.offerBalancerChange(msg.Chat.ID, msg.From.ID, 0, change)
			}
			return true
		}
	}
	s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
	return true
}
