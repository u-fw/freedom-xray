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

type balancerWizard struct {
	AdminID int64
	Step    string
	Tag     string
	ETag    string
	Expires time.Time
}

var balancerWizards = struct {
	sync.Mutex
	items map[int64]balancerWizard
}{items: map[int64]balancerWizard{}}

func clearBalancerWizard(chatID int64) {
	balancerWizards.Lock()
	delete(balancerWizards.items, chatID)
	balancerWizards.Unlock()
}

func setBalancerWizard(chatID int64, state balancerWizard) {
	balancerWizards.Lock()
	balancerWizards.items[chatID] = state
	balancerWizards.Unlock()
}

func getBalancerWizard(chatID, adminID int64) (balancerWizard, bool) {
	balancerWizards.Lock()
	defer balancerWizards.Unlock()
	state, ok := balancerWizards.items[chatID]
	if ok && time.Now().After(state.Expires) {
		delete(balancerWizards.items, chatID)
		return balancerWizard{}, false
	}
	return state, ok && state.AdminID == adminID
}

func balancerBackMarkup(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr(lang, "balancer.back"), "bl:page:0"),
		tgbotapi.NewInlineKeyboardButtonData(tr(lang, "routing.guide.back"), "rt:page:0")))
}

func balancerCallback(action, tag, etag string) string {
	return "bl:" + action + ":" + userCallbackKey(tag) + ":" + etag
}

func findBalancerByCallback(root map[string]any, key, etag string) (balancerRecord, error) {
	records, err := balancerRecords(root)
	if err != nil {
		return balancerRecord{}, err
	}
	var found balancerRecord
	count := 0
	for _, record := range records {
		if userCallbackKey(record.Tag) == key {
			found = record
			count++
		}
	}
	if count != 1 || found.ETag != etag {
		return balancerRecord{}, errBalancerChanged
	}
	return found, nil
}

func balancerStrategy(item map[string]any) string {
	strategy, _ := getMap(item["strategy"])
	kind, _ := strategy["type"].(string)
	if kind == "" {
		return "random"
	}
	return kind
}

func balancerMatchedOutbounds(root, item map[string]any) []string {
	selectors := stringList(item["selector"])
	entries, _ := root["outbounds"].([]any)
	var matched []string
	for _, entry := range entries {
		outbound, _ := getMap(entry)
		tag, _ := outbound["tag"].(string)
		if tag == "" || tag == networkAPITag(root) {
			continue
		}
		for _, selector := range selectors {
			if strings.HasPrefix(tag, selector) {
				matched = append(matched, tag)
				break
			}
		}
	}
	return matched
}

func balancerWarnings(lang string, root, item map[string]any) string {
	matched := balancerMatchedOutbounds(root, item)
	if len(matched) == 0 {
		return tr(lang, "balancer.warning.no_match")
	}
	strategy := strings.ToLower(balancerStrategy(item))
	if strategy != "leastping" && strategy != "leastload" {
		return ""
	}
	var selectors []string
	for _, kind := range []string{"observatory", "burstObservatory"} {
		config, _ := getMap(root[kind])
		selectors = append(selectors, stringList(config["subjectSelector"])...)
	}
	var missing []string
	for _, tag := range matched {
		covered := false
		for _, selector := range selectors {
			if strings.HasPrefix(tag, selector) {
				covered = true
				break
			}
		}
		if !covered {
			missing = append(missing, tag)
		}
	}
	var warning string
	if len(missing) > 0 {
		warning += tr(lang, "balancer.warning.unmonitored", mdEsc(strings.Join(missing, ", ")))
	}
	if fallback, _ := item["fallbackTag"].(string); fallback == "" {
		warning += tr(lang, "balancer.warning.no_fallback")
	}
	return warning
}

func (s *BotService) sendBalancerPage(chatID int64, msgID, offset int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "routing.error", mdEsc(err.Error())), balancerBackMarkup(s.cfg.Language))
		return
	}
	records, err := balancerRecords(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "routing.error", mdEsc(err.Error())), routingPageBackMarkup(s.cfg.Language))
		return
	}
	if offset < 0 || len(records) == 0 {
		offset = 0
	}
	if len(records) > 0 && offset >= len(records) {
		offset = ((len(records) - 1) / routingPageSize) * routingPageSize
	}
	end := offset + routingPageSize
	if end > len(records) {
		end = len(records)
	}
	body := tr(s.cfg.Language, "balancer.list", len(records))
	if notice != "" {
		body += "\n" + mdEsc(notice) + "\n"
	}
	if len(records) == 0 {
		body += tr(s.cfg.Language, "balancer.empty")
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, record := range records[offset:end] {
		label := fmt.Sprintf("%s · %s", record.Tag, balancerStrategy(record.Balancer))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(truncateLabel(label, 64), balancerCallback("v", record.Tag, record.ETag))))
	}
	var pages []tgbotapi.InlineKeyboardButton
	if offset > 0 {
		pages = append(pages, tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("bl:page:%d", offset-routingPageSize)))
	}
	if end < len(records) {
		pages = append(pages, tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.next"), fmt.Sprintf("bl:page:%d", end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	if s.cfg.AllowMutations {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "balancer.add"), "bl:add")))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "monitor.button"), "mo:page")))
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("bl:page:%d", offset)), tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "routing.guide.back"), "rt:page:0")))
	s.sendOrEdit(chatID, msgID, body, tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) sendBalancerDetail(chatID int64, msgID int, key, etag string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
		return
	}
	record, err := findBalancerByCallback(root, key, etag)
	if err != nil {
		s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
		return
	}
	item := record.Balancer
	selectors := strings.Join(stringList(item["selector"]), "\n")
	fallback, _ := item["fallbackTag"].(string)
	if fallback == "" {
		fallback = tr(s.cfg.Language, "routing.none")
	}
	matched := balancerMatchedOutbounds(root, item)
	matchedText := strings.Join(matched, ", ")
	if matchedText == "" {
		matchedText = tr(s.cfg.Language, "balancer.no_match")
	}
	body := tr(s.cfg.Language, "balancer.detail", mdEsc(record.Tag), mdEsc(selectors), mdEsc(balancerStrategy(item)), mdEsc(fallback), mdEsc(matchedText))
	body += balancerWarnings(s.cfg.Language, root, item)
	redacted := redactBalancerRecords([]balancerRecord{record})[0]
	if len(redacted.RedactedFields) > 0 {
		body += tr(s.cfg.Language, "balancer.hidden")
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	if s.cfg.AllowMutations {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "balancer.edit.selectors"), balancerCallback("selector", record.Tag, record.ETag)),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "balancer.edit.strategy"), balancerCallback("strategy", record.Tag, record.ETag))))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "balancer.edit.fallback"), balancerCallback("fallback", record.Tag, record.ETag)),
			tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "balancer.delete"), balancerCallback("delete", record.Tag, record.ETag))))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "balancer.back"), "bl:page:0"), tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, body, tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) offerBalancerChange(chatID, adminID int64, msgID int, change balancerChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), balancerBackMarkup(s.cfg.Language))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyBalancerChange(root, change)
	}
	if errors.Is(err, errBalancerChanged) {
		s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
		return
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "routing.edit.invalid", mdEsc(err.Error())), balancerBackMarkup(s.cfg.Language))
		return
	}
	var detail string
	switch change.Kind {
	case "put":
		detail = tr(s.cfg.Language, "balancer.confirm.create", mdEsc(change.Tag), mdEsc(strings.Join(stringList(change.Balancer["selector"]), ", ")))
	case "patch":
		field := change.Field
		switch field {
		case "selector":
			field = tr(s.cfg.Language, "balancer.edit.selectors")
		case "strategy":
			field = tr(s.cfg.Language, "balancer.edit.strategy")
		case "fallbackTag":
			field = tr(s.cfg.Language, "balancer.edit.fallback")
		}
		detail = tr(s.cfg.Language, "balancer.confirm.patch", mdEsc(change.Tag), mdEsc(field), mdEsc(previewRoutingValue(change.Value)))
	case "delete":
		detail = tr(s.cfg.Language, "balancer.confirm.delete", mdEsc(change.Tag))
	}
	if change.Kind != "delete" {
		_, item, err := findBalancer(root, change.Tag)
		if err == nil && item != nil {
			detail += balancerWarnings(s.cfg.Language, root, item)
		}
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{Balancer: &change}, detail)
}

func (s *BotService) handleBalancerInput(msg *tgbotapi.Message) bool {
	if msg.Chat == nil || msg.From == nil {
		return false
	}
	state, ok := getBalancerWizard(msg.Chat.ID, msg.From.ID)
	if !ok {
		return false
	}
	if msg.Command() == "cancel" {
		clearBalancerWizard(msg.Chat.ID)
		if strings.HasPrefix(state.Step, "monitor-") {
			s.sendObservatoryPage(msg.Chat.ID, 0, tr(s.cfg.Language, "wizard.cancelled"))
		} else {
			s.sendBalancerPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "wizard.cancelled"))
		}
		return true
	}
	if msg.Command() != "" {
		return false
	}
	if !s.cfg.AllowMutations {
		clearBalancerWizard(msg.Chat.ID)
		s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	value := strings.TrimSpace(msg.Text)
	if state.Step == "monitor-interval" || state.Step == "monitor-url" {
		field := strings.TrimPrefix(state.Step, "monitor-")
		change := observatoryChange{Kind: state.Tag, ETag: state.ETag, Action: "field", Field: field, Value: value}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err == nil {
			_, err = applyObservatoryChange(root, change)
		}
		if errors.Is(err, errObservatoryChanged) {
			clearBalancerWizard(msg.Chat.ID)
			s.sendObservatoryPage(msg.Chat.ID, 0, tr(s.cfg.Language, "monitor.changed"))
			return true
		}
		if err != nil {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
			return true
		}
		clearBalancerWizard(msg.Chat.ID)
		s.offerObservatoryChange(msg.Chat.ID, msg.From.ID, 0, change)
		return true
	}
	if state.Step == "new-tag" {
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "balancer.invalid_tag"))
			return true
		}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.error", mdEsc(err.Error())))
			return true
		}
		index, _, err := findBalancer(root, value)
		if err != nil || index >= 0 {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "balancer.duplicate"))
			return true
		}
		state.Tag, state.Step, state.Expires = value, "new-selector", time.Now().Add(5*time.Minute)
		setBalancerWizard(msg.Chat.ID, state)
		s.sendOrEdit(msg.Chat.ID, 0, tr(s.cfg.Language, "balancer.selector_prompt"), balancerBackMarkup(s.cfg.Language))
		return true
	}
	var selectors []any
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			selectors = append(selectors, line)
		}
	}
	if len(selectors) == 0 {
		s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "balancer.selector_prompt"))
		return true
	}
	if state.Step == "monitor-selectors" {
		change := observatoryChange{Kind: state.Tag, Action: "selectors", ETag: state.ETag, Selectors: selectors}
		if state.ETag == "" {
			change.Action = "put"
			change.Config = map[string]any{"subjectSelector": selectors}
			if state.Tag == "observatory" {
				change.Config["probeInterval"] = "1m"
			} else {
				change.Config["pingConfig"] = map[string]any{}
			}
		}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err == nil {
			_, err = applyObservatoryChange(root, change)
		}
		if errors.Is(err, errObservatoryChanged) {
			clearBalancerWizard(msg.Chat.ID)
			s.sendObservatoryPage(msg.Chat.ID, 0, tr(s.cfg.Language, "monitor.changed"))
			return true
		}
		if err != nil {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
			return true
		}
		clearBalancerWizard(msg.Chat.ID)
		s.offerObservatoryChange(msg.Chat.ID, msg.From.ID, 0, change)
		return true
	}
	change := balancerChange{Tag: state.Tag, ETag: state.ETag, Kind: "patch", Field: "selector", Value: selectors}
	if state.Step == "new-selector" {
		change.Kind = "put"
		change.Balancer = map[string]any{"tag": state.Tag, "selector": selectors, "strategy": map[string]any{"type": "random"}}
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyBalancerChange(root, change)
	}
	if errors.Is(err, errBalancerChanged) {
		clearBalancerWizard(msg.Chat.ID)
		s.sendBalancerPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "balancer.changed"))
		return true
	}
	if err != nil {
		s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	clearBalancerWizard(msg.Chat.ID)
	s.offerBalancerChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func (s *BotService) handleBalancerCallback(cb *tgbotapi.CallbackQuery) {
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
			clearRoutingWizard(chatID)
			s.sendBalancerPage(chatID, msgID, offset, "")
		}
		return
	}
	if parts[1] == "add" && len(parts) == 2 {
		if !s.cfg.AllowMutations {
			s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), balancerBackMarkup(s.cfg.Language))
			return
		}
		clearOtherRoutingInputs(chatID)
		setBalancerWizard(chatID, balancerWizard{AdminID: cb.From.ID, Step: "new-tag", Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "balancer.tag_prompt"), balancerBackMarkup(s.cfg.Language))
		return
	}
	if len(parts) < 4 {
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
		return
	}
	record, err := findBalancerByCallback(root, parts[2], parts[3])
	if err != nil {
		s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
		return
	}
	if parts[1] == "v" && len(parts) == 4 {
		clearRoutingWizard(chatID)
		s.sendBalancerDetail(chatID, msgID, parts[2], parts[3])
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), balancerBackMarkup(s.cfg.Language))
		return
	}
	switch parts[1] {
	case "selector":
		if len(parts) != 4 {
			return
		}
		clearOtherRoutingInputs(chatID)
		setBalancerWizard(chatID, balancerWizard{AdminID: cb.From.ID, Step: "edit-selector", Tag: record.Tag, ETag: record.ETag, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "balancer.selector_prompt"), balancerBackMarkup(s.cfg.Language))
	case "strategy":
		if len(parts) != 4 {
			return
		}
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, choices := range [][]string{{"random", "roundRobin"}, {"leastPing", "leastLoad"}} {
			var buttons []tgbotapi.InlineKeyboardButton
			for _, choice := range choices {
				buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(choice, balancerCallback("ss", record.Tag, record.ETag)+":"+choice))
			}
			rows = append(rows, buttons)
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.back"), balancerCallback("v", record.Tag, record.ETag))))
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "balancer.strategy_prompt"), tgbotapi.NewInlineKeyboardMarkup(rows...))
	case "ss":
		if len(parts) != 5 {
			return
		}
		s.offerBalancerChange(chatID, cb.From.ID, msgID, balancerChange{Kind: "patch", Tag: record.Tag, ETag: record.ETag, Field: "strategy", Value: parts[4]})
	case "fallback":
		if len(parts) != 4 && len(parts) != 5 {
			return
		}
		offset := 0
		if len(parts) == 5 {
			offset, err = strconv.Atoi(parts[4])
			if err != nil {
				return
			}
		}
		var targets []routingTarget
		for _, target := range routingTargets(root) {
			if target.Kind == "outboundTag" {
				targets = append(targets, target)
			}
		}
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
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, target := range targets[offset:end] {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(truncateLabel(target.Tag, 50), balancerCallback("sf", record.Tag, record.ETag)+":"+routingTargetKey(target))))
		}
		var pages []tgbotapi.InlineKeyboardButton
		if offset > 0 {
			pages = append(pages, tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("%s:%d", balancerCallback("fallback", record.Tag, record.ETag), offset-routingPageSize)))
		}
		if end < len(targets) {
			pages = append(pages, tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.next"), fmt.Sprintf("%s:%d", balancerCallback("fallback", record.Tag, record.ETag), end)))
		}
		if len(pages) > 0 {
			rows = append(rows, pages)
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "balancer.clear_fallback"), balancerCallback("sf", record.Tag, record.ETag)+":none")))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.back"), balancerCallback("v", record.Tag, record.ETag))))
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "balancer.fallback_prompt"), tgbotapi.NewInlineKeyboardMarkup(rows...))
	case "sf":
		if len(parts) != 5 {
			return
		}
		var value any
		if parts[4] != "none" {
			target, ok := findRoutingTarget(root, parts[4])
			if !ok || target.Kind != "outboundTag" {
				s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
				return
			}
			value = target.Tag
		}
		s.offerBalancerChange(chatID, cb.From.ID, msgID, balancerChange{Kind: "patch", Tag: record.Tag, ETag: record.ETag, Field: "fallbackTag", Value: value})
	case "delete":
		if len(parts) != 4 {
			return
		}
		s.offerBalancerChange(chatID, cb.From.ID, msgID, balancerChange{Kind: "delete", Tag: record.Tag, ETag: record.ETag})
	}
}
