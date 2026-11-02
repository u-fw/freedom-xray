package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
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

func consumeBalancerWizard(chatID int64, expected balancerWizard) bool {
	balancerWizards.Lock()
	defer balancerWizards.Unlock()
	current, ok := balancerWizards.items[chatID]
	if !ok || current != expected {
		return false
	}
	delete(balancerWizards.items, chatID)
	return time.Now().Before(current.Expires)
}

func balancerBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(
		newButton(tr(lang, "balancer.back"), "bl:page:0"),
		newButton(tr(lang, "routing.guide.back"), "rt:page:0")))
}

func balancerCallback(action, tag, etag string) string {
	return "bl:" + action + ":" + userCallbackKey(tag) + ":" + etag
}

func findBalancerByCallback(root map[string]any, key, etag string) (balancerRecord, error) {
	found, err := findBalancerByKey(root, key)
	if err == nil && found.ETag != etag {
		err = errBalancerChanged
	}
	return found, err
}

func findBalancerByKey(root map[string]any, key string) (balancerRecord, error) {
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
	if count != 1 {
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

func balancerWarnings(lang string, root, item map[string]any) htmlText {
	matched := balancerMatchedOutbounds(root, item)
	if len(matched) == 0 {
		return trh(lang, "balancer.warning.no_match")
	}
	strategy := strings.ToLower(balancerStrategy(item))
	fallback, _ := item["fallbackTag"].(string)
	if strategy != "leastping" && strategy != "leastload" && fallback == "" {
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
	var warning htmlText
	if len(missing) > 0 {
		warning += trh(lang, "balancer.warning.unmonitored", strings.Join(missing, ", "))
	}
	if fallback == "" {
		warning += trh(lang, "balancer.warning.no_fallback")
	}
	return warning
}

func (s *BotService) sendBalancerPage(chatID int64, msgID, offset int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), balancerBackMarkup(s.cfg.Language))
		return
	}
	records, err := balancerRecords(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), routingPageBackMarkup(s.cfg.Language))
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
	body := trh(s.cfg.Language, "balancer.list", len(records))
	if notice != "" {
		body += "\n" + esc(notice) + "\n"
	}
	if len(records) == 0 {
		body += trh(s.cfg.Language, "balancer.empty")
	}
	var rows [][]tgButton
	for _, record := range records[offset:end] {
		label := fmt.Sprintf("%s · %s", record.Tag, balancerStrategy(record.Balancer))
		rows = append(rows, newRow(newButton(truncateLabel(label, 64), balancerCallback("v", record.Tag, record.ETag))))
	}
	var pages []tgButton
	if offset > 0 {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("bl:page:%d", offset-routingPageSize)))
	}
	if end < len(records) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), fmt.Sprintf("bl:page:%d", end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "balancer.add"), "bl:add"),
			newButton(tr(s.cfg.Language, "balancer.native.add"), "bl:jsonnew")))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "monitor.button"), "mo:page")))
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("bl:page:%d", offset)), newButton(tr(s.cfg.Language, "routing.guide.back"), "rt:page:0")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendBalancerDetail(chatID int64, msgID int, key, etag string) {
	s.sendBalancerDetailNotice(chatID, msgID, key, etag, "")
}

func (s *BotService) returnBalancerDetail(chatID int64, msgID int, tag, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		records, err := balancerRecords(root)
		if err == nil {
			for _, record := range records {
				if record.Tag == tag {
					s.sendBalancerDetailNotice(chatID, msgID, userCallbackKey(tag), record.ETag, notice)
					return
				}
			}
		}
	}
	s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
}

func (s *BotService) sendBalancerDetailNotice(chatID int64, msgID int, key, etag, notice string) {
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
	body := trh(s.cfg.Language, "balancer.detail", record.Tag, selectors, balancerStrategy(item), fallback, matchedText)
	if notice != "" {
		body = esc(notice) + "\n\n" + body
	}
	body += balancerWarnings(s.cfg.Language, root, item)
	redacted := redactBalancerRecords([]balancerRecord{record})[0]
	if len(redacted.RedactedFields) > 0 {
		body += trh(s.cfg.Language, "balancer.hidden")
	}
	var rows [][]tgButton
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "balancer.live"), balancerCallback("live", record.Tag, record.ETag))))
	if s.cfg.AllowMutations {
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "balancer.edit.selectors"), balancerCallback("selector", record.Tag, record.ETag)),
			newButton(tr(s.cfg.Language, "balancer.edit.strategy"), balancerCallback("strategy", record.Tag, record.ETag))))
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "balancer.edit.fallback"), balancerCallback("fallback", record.Tag, record.ETag)),
			newButton(tr(s.cfg.Language, "balancer.delete"), balancerCallback("delete", record.Tag, record.ETag))))
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "balancer.native.edit"), balancerCallback("json", record.Tag, record.ETag))))
	}
	if s.cfg.AllowSensitiveBackup {
		index, _, err := findBalancer(root, record.Tag)
		if err == nil && index >= 0 {
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "config.export.button"), nativeConfigExportCallback("balancer", index+1, record.ETag))))
		}
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "balancer.back"), "bl:page:0"), newButton(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendBalancerLive(chatID int64, msgID int, record balancerRecord) {
	s.sendBalancerLiveNotice(chatID, msgID, record, "")
}

func (s *BotService) sendBalancerLiveNotice(chatID int64, msgID int, record balancerRecord, notice string) {
	status, err := queryBalancerLive(s.cfg, record.Tag)
	body := trh(s.cfg.Language, "balancer.live.title", record.Tag)
	if notice != "" {
		body = esc(notice) + "\n\n" + body
	}
	var rows [][]tgButton
	if errors.Is(err, errRoutingServiceDisabled) {
		body += trh(s.cfg.Language, "balancer.live.disabled")
		if s.cfg.AllowMutations {
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "balancer.live.enable"), balancerCallback("enable", record.Tag, record.ETag))))
		}
	} else if err != nil {
		body += trh(s.cfg.Language, "balancer.live.failed", err.Error())
	} else {
		selected := strings.Join(status.Selected, ", ")
		if selected == "" {
			selected = tr(s.cfg.Language, "balancer.live.none")
		}
		override := status.Override
		if override == "" {
			override = tr(s.cfg.Language, "balancer.override.auto")
		}
		body += trh(s.cfg.Language, "balancer.live.result", selected, override)
		body += trh(s.cfg.Language, "balancer.override.runtime_note")
		if s.cfg.AllowMutations {
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "balancer.override.button"), balancerCallback("override", record.Tag, record.ETag))))
		}
	}
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "common.refresh"), balancerCallback("live", record.Tag, record.ETag)),
		newButton(tr(s.cfg.Language, "common.back"), balancerCallback("v", record.Tag, record.ETag))))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) returnToBalancerLive(chatID int64, msgID int, tag, etag string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		var record balancerRecord
		record, err = findBalancerByCallback(root, userCallbackKey(tag), etag)
		if err == nil {
			s.sendBalancerLive(chatID, msgID, record)
			return
		}
	}
	s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
}

func (s *BotService) offerBalancerChange(chatID, adminID int64, msgID int, change balancerChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), balancerBackMarkup(s.cfg.Language))
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
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), balancerBackMarkup(s.cfg.Language))
		return
	}
	var detail htmlText
	switch change.Kind {
	case "put":
		key := "balancer.confirm.create"
		if change.ETag != "" {
			key = "balancer.native.confirm_replace"
		}
		detail = trh(s.cfg.Language, key, change.Tag, strings.Join(stringList(change.Balancer["selector"]), ", "))
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
		detail = trh(s.cfg.Language, "balancer.confirm.patch", change.Tag, field, previewRoutingValue(change.Value))
	case "delete":
		detail = trh(s.cfg.Language, "balancer.confirm.delete", change.Tag)
	}
	if change.Kind != "delete" {
		_, item, err := findBalancer(root, change.Tag)
		if err == nil && item != nil {
			detail += balancerWarnings(s.cfg.Language, root, item)
		}
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{Balancer: &change}, detail)
}

func (s *BotService) handleBalancerInput(msg *tgMessage) bool {
	if msg.Chat == nil || msg.From == nil {
		return false
	}
	state, ok := getBalancerWizard(msg.Chat.ID, msg.From.ID)
	if !ok {
		return false
	}
	if msg.Command() == "cancel" {
		if !consumeBalancerWizard(msg.Chat.ID, state) {
			return true
		}
		if strings.HasPrefix(state.Step, "monitor-") {
			s.returnObservatoryDetail(msg.Chat.ID, 0, state.Tag, tr(s.cfg.Language, "wizard.cancelled"))
		} else if state.ETag != "" {
			s.returnBalancerDetail(msg.Chat.ID, 0, state.Tag, tr(s.cfg.Language, "wizard.cancelled"))
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
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	if state.Step == "native-create" || state.Step == "native-replace" || state.Step == "monitor-native" {
		return s.handleNativeBalancingInput(msg, state)
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
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
			return true
		}
		clearBalancerWizard(msg.Chat.ID)
		s.offerObservatoryChange(msg.Chat.ID, msg.From.ID, 0, change)
		return true
	}
	if state.Step == "new-tag" {
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "balancer.invalid_tag"))
			return true
		}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.error", err.Error()))
			return true
		}
		index, _, err := findBalancer(root, value)
		if err != nil || index >= 0 {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "balancer.duplicate"))
			return true
		}
		state.Tag, state.Step, state.Expires = value, "new-selector", time.Now().Add(5*time.Minute)
		setBalancerWizard(msg.Chat.ID, state)
		s.sendOrEdit(msg.Chat.ID, 0, trh(s.cfg.Language, "balancer.selector_prompt"), balancerBackMarkup(s.cfg.Language))
		return true
	}
	var selectors []any
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			selectors = append(selectors, line)
		}
	}
	if len(selectors) == 0 {
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "balancer.selector_prompt"))
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
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
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
		s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	clearBalancerWizard(msg.Chat.ID)
	s.offerBalancerChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func (s *BotService) handleBalancerCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	parts := strings.Split(cb.Data, ":")
	if len(parts) < 2 {
		return
	}
	if parts[1] == "ol" || parts[1] == "op" {
		s.handleBalancerOverrideCallback(cb)
		return
	}
	if parts[1] == "page" && len(parts) == 3 {
		offset, err := strconv.Atoi(parts[2])
		if err == nil {
			clearInputSessions(chatID)
			s.sendBalancerPage(chatID, msgID, offset, "")
		}
		return
	}
	if (parts[1] == "add" || parts[1] == "jsonnew") && len(parts) == 2 {
		if !s.cfg.AllowMutations {
			s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), balancerBackMarkup(s.cfg.Language))
			return
		}
		clearInputSessions(chatID)
		step, prompt := "new-tag", "balancer.tag_prompt"
		if parts[1] == "jsonnew" {
			step, prompt = "native-create", "balancer.native.create_prompt"
		}
		setBalancerWizard(chatID, balancerWizard{AdminID: cb.From.ID, Step: step, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, prompt), balancerBackMarkup(s.cfg.Language))
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
		clearInputSessions(chatID)
		s.sendBalancerDetail(chatID, msgID, parts[2], parts[3])
		return
	}
	if parts[1] == "live" && len(parts) == 4 {
		s.sendBalancerLive(chatID, msgID, record)
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), balancerBackMarkup(s.cfg.Language))
		return
	}
	switch parts[1] {
	case "override":
		if len(parts) != 4 {
			return
		}
		clearInputSessions(chatID)
		status, err := queryBalancerLive(s.cfg, record.Tag)
		if err != nil {
			s.sendBalancerLiveNotice(chatID, msgID, record, err.Error())
			return
		}
		s.sendBalancerOverridePicker(chatID, msgID, record, status, 0)
	case "json":
		if len(parts) != 4 {
			return
		}
		clearInputSessions(chatID)
		setBalancerWizard(chatID, balancerWizard{AdminID: cb.From.ID, Step: "native-replace", Tag: record.Tag, ETag: record.ETag, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "balancer.native.replace_prompt", record.Tag), balancerBackMarkup(s.cfg.Language))
	case "enable":
		if len(parts) != 4 {
			return
		}
		server := s.cfg.StatsAPIServer
		if server == "" {
			server = "127.0.0.1:10085"
		}
		changed, err := applyRoutingService(root, server)
		if err != nil {
			s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), balancerBackMarkup(s.cfg.Language))
			return
		}
		if !changed {
			s.sendBalancerLive(chatID, msgID, record)
			return
		}
		s.queueGuidedRoutingChange(chatID, cb.From.ID, msgID, pendingRoutingChange{EnableRoutingAPI: true, LiveBalancerTag: record.Tag, LiveBalancerETag: record.ETag}, trh(s.cfg.Language, "balancer.live.enable_confirm"))
	case "selector":
		if len(parts) != 4 {
			return
		}
		clearInputSessions(chatID)
		setBalancerWizard(chatID, balancerWizard{AdminID: cb.From.ID, Step: "edit-selector", Tag: record.Tag, ETag: record.ETag, Expires: time.Now().Add(5 * time.Minute)})
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "balancer.selector_prompt"), balancerBackMarkup(s.cfg.Language))
	case "strategy":
		if len(parts) != 4 {
			return
		}
		var rows [][]tgButton
		for _, choices := range [][]string{{"random", "roundRobin"}, {"leastPing", "leastLoad"}} {
			var buttons []tgButton
			for _, choice := range choices {
				buttons = append(buttons, newButton(tr(s.cfg.Language, "balancer.strategy."+choice), balancerCallback("ss", record.Tag, record.ETag)+":"+choice))
			}
			rows = append(rows, buttons)
		}
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.back"), balancerCallback("v", record.Tag, record.ETag))))
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "balancer.strategy_prompt"), newKeyboard(rows...))
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
		var rows [][]tgButton
		for _, target := range targets[offset:end] {
			rows = append(rows, newRow(newButton(truncateLabel(target.Tag, 50), balancerCallback("sf", record.Tag, record.ETag)+":"+routingTargetKey(target))))
		}
		var pages []tgButton
		if offset > 0 {
			pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("%s:%d", balancerCallback("fallback", record.Tag, record.ETag), offset-routingPageSize)))
		}
		if end < len(targets) {
			pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), fmt.Sprintf("%s:%d", balancerCallback("fallback", record.Tag, record.ETag), end)))
		}
		if len(pages) > 0 {
			rows = append(rows, pages)
		}
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "balancer.clear_fallback"), balancerCallback("sf", record.Tag, record.ETag)+":none")))
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.back"), balancerCallback("v", record.Tag, record.ETag))))
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "balancer.fallback_prompt"), newKeyboard(rows...))
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
