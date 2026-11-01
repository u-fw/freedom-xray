package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const routingPageSize = 6

type guidedRoutingWizard struct {
	AdminID int64
	Step    string
	Field   string
	Ref     routingRuleRef
	Rule    map[string]any
	Expires time.Time
}

var guidedRoutingWizards = struct {
	sync.Mutex
	items map[int64]guidedRoutingWizard
}{items: map[int64]guidedRoutingWizard{}}

func clearGuidedRoutingWizard(chatID int64) {
	guidedRoutingWizards.Lock()
	delete(guidedRoutingWizards.items, chatID)
	guidedRoutingWizards.Unlock()
}

func routingPageBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(
		newButton(tr(lang, "routing.guide.back"), "rt:page:0"),
		newButton(tr(lang, "menu.home"), "menu_main")))
}

func routingRefData(action string, ref routingRuleRef) string {
	return fmt.Sprintf("rt:%s:%d:%s", action, ref.Index, ref.ETag)
}

func parseRoutingRef(index, etag string) (routingRuleRef, error) {
	n, err := strconv.Atoi(index)
	if err != nil || n < 1 || len(etag) != 24 {
		return routingRuleRef{}, fmt.Errorf("invalid rule reference")
	}
	if _, err = hex.DecodeString(etag); err != nil {
		return routingRuleRef{}, fmt.Errorf("invalid rule reference")
	}
	return routingRuleRef{Index: n, ETag: etag}, nil
}

func routingFieldLabel(lang, field string) string {
	key := "routing.field." + field
	label := tr(lang, key)
	if label == key {
		return field
	}
	return label
}

func routingRuleTitle(rule map[string]any, index int) string {
	if tag, ok := rule["ruleTag"].(string); ok && tag != "" {
		return tag
	}
	for _, key := range []string{"domains", "domain", "ip", "inboundTag", "protocol", "user", "port", "network", "sourceIP", "source"} {
		if values := stringList(rule[key]); len(values) > 0 {
			return values[0]
		}
	}
	return fmt.Sprintf("#%d", index)
}

func routingRuleTarget(rule map[string]any) string {
	if tag, ok := rule["outboundTag"].(string); ok && tag != "" {
		return tag
	}
	if tag, ok := rule["balancerTag"].(string); ok && tag != "" {
		return "⚖ " + tag
	}
	return "—"
}

func routingRuleValueLines(value any) []string {
	var result []string
	var add func(any)
	add = func(value any) {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				add(item)
			}
		case string:
			result = append(result, strings.ReplaceAll(strings.ReplaceAll(v, "\r", "\\r"), "\n", "\\n"))
		default:
			raw, err := json.Marshal(v)
			if err == nil {
				result = append(result, string(raw))
			}
		}
	}
	add(value)
	if len(result) == 0 {
		return []string{"—"}
	}
	return result
}

func routingRuleDetailText(lang string, record routingRuleRecord, root map[string]any) htmlText {
	var body htmlBuilder
	body.Add(trh(lang, "routing.guide.detail", record.Index, routingRuleTitle(record.Rule, record.Index)))
	if record.Protected {
		body.Add(trh(lang, "routing.guide.protected"))
	}
	ordered := []string{"type", "ruleTag", "inboundTag", "domain", "domains", "ip", "port", "network", "protocol", "source", "sourceIP", "sourcePort", "localIP", "localPort", "user", "vlessRoute", "process", "localOS", "outboundTag", "balancerTag"}
	seen := map[string]bool{}
	for key := range record.Rule {
		seen[key] = true
	}
	var extra []string
	for key := range seen {
		found := false
		for _, known := range ordered {
			if key == known {
				found = true
				break
			}
		}
		if !found {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	ordered = append(ordered, extra...)
	for _, field := range ordered {
		value, ok := record.Rule[field]
		if !ok {
			continue
		}
		body.Add("\n<b>")
		body.Add(esc(routingFieldLabel(lang, field)))
		if field == "domain" && record.Rule["domains"] != nil || field == "source" && record.Rule["sourceIP"] != nil {
			body.Add(" ")
			body.Add(esc(tr(lang, "routing.guide.overridden")))
		}
		body.Add(":</b>\n")
		if !routingPublicFields[field] {
			body.Add("• ", trh(lang, "routing.guide.hidden"), "\n")
			continue
		}
		for _, line := range routingRuleValueLines(value) {
			body.Add("• ")
			body.Add(esc(line))
			body.Add("\n")
		}
	}
	if tag, ok := record.Rule["outboundTag"].(string); ok {
		outbounds, _ := root["outbounds"].([]any)
		for _, entry := range outbounds {
			item, _ := getMap(entry)
			if item["tag"] == tag {
				if protocol, ok := item["protocol"].(string); ok && protocol != "" {
					body.Add(trh(lang, "routing.guide.outboundprotocol", protocol))
				}
				break
			}
		}
	}
	return body.HTML()
}

func (s *BotService) sendRoutingPage(chatID int64, msgID, offset int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), s.backToMainMarkup())
		return
	}
	records, err := routingRuleRecords(root)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), s.backToMainMarkup())
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
	routing, _ := getMap(root["routing"])
	script, _ := routing["script"].(string)
	strategy, _ := routing["domainStrategy"].(string)
	if strategy == "" {
		strategy = "default"
	}
	var body htmlBuilder
	body.Add(trh(s.cfg.Language, "routing.guide.list", strategy, len(records)))
	if script != "" {
		body.Add(trh(s.cfg.Language, "routing.guide.script"))
	}
	if notice != "" {
		body.Add("\n")
		body.Add(esc(notice))
		body.Add("\n")
	}
	if len(records) == 0 {
		body.Add(trh(s.cfg.Language, "routing.empty"))
	} else {
		body.Add(trh(s.cfg.Language, "routing.guide.page", offset+1, end, len(records)))
	}
	rows := [][]tgButton{}
	for _, record := range records[offset:end] {
		label := fmt.Sprintf("%d. %s → %s", record.Index, routingRuleTitle(record.Rule, record.Index), routingRuleTarget(record.Rule))
		if record.Protected {
			label = "🔒 " + label
		}
		ref := routingRuleRef{Index: record.Index, ETag: record.ETag}
		rows = append(rows, newRow(newButton(truncateLabel(label, 64), routingRefData("v", ref))))
	}
	var pages []tgButton
	if offset > 0 {
		prev := offset - routingPageSize
		if prev < 0 {
			prev = 0
		}
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), fmt.Sprintf("rt:page:%d", prev)))
	}
	if end < len(records) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), fmt.Sprintf("rt:page:%d", end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "outbound.button"), "ob:page:0"), newButton(tr(s.cfg.Language, "balancer.button"), "bl:page:0"), newButton(tr(s.cfg.Language, "dns.button"), "dn:page:0")))
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "route.test.button"), "rt:test")))
	if s.cfg.AllowMutations && script == "" {
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "routing.guide.add"), "rt:add"),
			newButton(tr(s.cfg.Language, "routing.guide.advanced"), "rt:advanced")))
	}
	if s.cfg.AllowMutations && script != "" {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "routing.guide.advanced"), "rt:advanced")))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.refresh"), fmt.Sprintf("rt:page:%d", offset)), newButton(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, body.HTML(), newKeyboard(rows...))
}

func (s *BotService) sendRoutingRuleDetail(chatID int64, msgID int, ref routingRuleRef) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = lookupRoutingRule(root, ref)
	}
	if err != nil {
		s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
		return
	}
	records, err := routingRuleRecords(root)
	if err != nil {
		s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
		return
	}
	record := records[ref.Index-1]
	rows := [][]tgButton{}
	routing, _ := getMap(root["routing"])
	script, _ := routing["script"].(string)
	if s.cfg.AllowMutations && !record.Protected && script == "" {
		button := func(field string) tgButton {
			return newButton(tr(s.cfg.Language, "routing.field."+field), fmt.Sprintf("rt:f:%s:%d:%s", field, ref.Index, ref.ETag))
		}
		domainField := "domain"
		if record.Rule["domains"] != nil {
			domainField = "domains"
		}
		rows = append(rows, newRow(button(domainField), button("ip"), button("port")))
		rows = append(rows, newRow(button("inboundTag"), button("network"), button("protocol")))
		sourceField := "source"
		if record.Rule["sourceIP"] != nil {
			sourceField = "sourceIP"
		}
		rows = append(rows, newRow(button(sourceField), button("user"), button("ruleTag")))
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "routing.guide.more_fields"), routingRefData("more", ref))))
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "routing.guide.target"), routingRefData("t", ref))))
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "routing.guide.up"), routingRefData("up", ref)),
			newButton(tr(s.cfg.Language, "routing.guide.down"), routingRefData("down", ref)),
			newButton(tr(s.cfg.Language, "routing.guide.delete"), routingRefData("del", ref))))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "config.export.button"), nativeConfigExportCallback("rule", ref.Index, ref.ETag))))
	offset := ((ref.Index - 1) / routingPageSize) * routingPageSize
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "routing.guide.back"), fmt.Sprintf("rt:page:%d", offset)), newButton(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, routingRuleDetailText(s.cfg.Language, record, root), newKeyboard(rows...))
}

func (s *BotService) sendRoutingMoreFields(chatID int64, msgID int, ref routingRuleRef) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
		return
	}
	rule, err := lookupRoutingRule(root, ref)
	if err != nil || protectedRoutingRule(root, rule) || !s.cfg.AllowMutations {
		s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
		return
	}
	button := func(field string) tgButton {
		return newButton(tr(s.cfg.Language, "routing.field."+field), fmt.Sprintf("rt:f:%s:%d:%s", field, ref.Index, ref.ETag))
	}
	rows := [][]tgButton{
		newRow(button("sourcePort"), button("localIP")),
		newRow(button("localPort"), button("vlessRoute")),
		newRow(newButton(tr(s.cfg.Language, "common.back"), routingRefData("v", ref))),
	}
	s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.guide.more_fields_text"), newKeyboard(rows...))
}

type routingTarget struct{ Kind, Tag, Protocol string }

func routingTargets(root map[string]any) []routingTarget {
	result := []routingTarget{}
	outbounds, _ := root["outbounds"].([]any)
	for _, entry := range outbounds {
		item, _ := getMap(entry)
		tag, _ := item["tag"].(string)
		protocol, _ := item["protocol"].(string)
		if tag != "" && tag != networkAPITag(root) {
			result = append(result, routingTarget{Kind: "outboundTag", Tag: tag, Protocol: protocol})
		}
	}
	routing, _ := getMap(root["routing"])
	balancers, _ := routing["balancers"].([]any)
	for _, entry := range balancers {
		item, _ := getMap(entry)
		tag, _ := item["tag"].(string)
		if tag != "" {
			result = append(result, routingTarget{Kind: "balancerTag", Tag: tag})
		}
	}
	return result
}

func routingTargetKey(target routingTarget) string {
	return userCallbackKey(target.Kind + "\x00" + target.Tag)
}
func findRoutingTarget(root map[string]any, key string) (routingTarget, bool) {
	var found routingTarget
	count := 0
	for _, target := range routingTargets(root) {
		if routingTargetKey(target) == key {
			found = target
			count++
		}
	}
	return found, count == 1
}

func (s *BotService) sendRoutingTargets(cb *tgCallback, ref *routingRuleRef, offset int) {
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil && ref != nil {
		_, err = lookupRoutingRule(root, *ref)
	}
	if err != nil {
		s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
		return
	}
	targets := routingTargets(root)
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
	rows := [][]tgButton{}
	for _, target := range targets[offset:end] {
		label := target.Tag
		if target.Kind == "balancerTag" {
			label = "⚖ " + label
		} else if target.Protocol != "" {
			label += " (" + target.Protocol + ")"
		}
		data := "rt:np:" + routingTargetKey(target)
		if ref != nil {
			data = fmt.Sprintf("rt:p:%d:%s:%s", ref.Index, ref.ETag, routingTargetKey(target))
		}
		rows = append(rows, newRow(newButton(truncateLabel(label, 60), data)))
	}
	var pages []tgButton
	pageData := func(next int) string {
		if ref == nil {
			return fmt.Sprintf("rt:nt:%d", next)
		}
		return fmt.Sprintf("rt:t:%d:%s:%d", ref.Index, ref.ETag, next)
	}
	if offset > 0 {
		prev := offset - routingPageSize
		if prev < 0 {
			prev = 0
		}
		pages = append(pages, newButton(tr(s.cfg.Language, "common.prev"), pageData(prev)))
	}
	if end < len(targets) {
		pages = append(pages, newButton(tr(s.cfg.Language, "common.next"), pageData(end)))
	}
	if len(pages) > 0 {
		rows = append(rows, pages)
	}
	back := "rt:add"
	if ref != nil {
		back = routingRefData("v", *ref)
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "common.back"), back)))
	body := trh(s.cfg.Language, "routing.guide.choose_target")
	if len(targets) == 0 {
		body += "\n" + trh(s.cfg.Language, "routing.guide.no_targets")
	}
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) sendRoutingAdvanced(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendRoutingPage(chatID, msgID, 0, "")
		return
	}
	rows := [][]tgButton{
		newRow(newButton(tr(s.cfg.Language, "routing.edit.add"), "routing_manage:add"), newButton(tr(s.cfg.Language, "routing.edit.update"), "routing_manage:update"), newButton(tr(s.cfg.Language, "routing.edit.delete"), "routing_manage:delete")),
		newRow(newButton(tr(s.cfg.Language, "network.outbound.put"), "routing_manage:outbound_put"), newButton(tr(s.cfg.Language, "network.outbound.delete"), "routing_manage:outbound_delete")),
		newRow(newButton(tr(s.cfg.Language, "network.dns.put"), "routing_manage:dns_put")),
		newRow(newButton(tr(s.cfg.Language, "routing.guide.back"), "rt:page:0")),
	}
	s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.guide.advanced_text"), newKeyboard(rows...))
}

func (s *BotService) sendRoutingAddPicker(chatID int64, msgID int) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), routingPageBackMarkup(s.cfg.Language))
		return
	}
	rows := [][]tgButton{
		newRow(newButton(tr(s.cfg.Language, "routing.field.domain"), "rt:nf:domain"), newButton(tr(s.cfg.Language, "routing.field.ip"), "rt:nf:ip")),
		newRow(newButton(tr(s.cfg.Language, "routing.field.port"), "rt:nf:port"), newButton(tr(s.cfg.Language, "routing.field.inboundTag"), "rt:nf:inboundTag")),
		newRow(newButton(tr(s.cfg.Language, "routing.guide.back"), "rt:page:0")),
	}
	s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.guide.add_picker"), newKeyboard(rows...))
}

func setGuidedRoutingWizard(chatID int64, state guidedRoutingWizard) {
	guidedRoutingWizards.Lock()
	guidedRoutingWizards.items[chatID] = state
	guidedRoutingWizards.Unlock()
}
func getGuidedRoutingWizard(chatID, adminID int64, step string) (guidedRoutingWizard, bool) {
	guidedRoutingWizards.Lock()
	defer guidedRoutingWizards.Unlock()
	state, ok := guidedRoutingWizards.items[chatID]
	if ok && time.Now().After(state.Expires) {
		delete(guidedRoutingWizards.items, chatID)
		return guidedRoutingWizard{}, false
	}
	return state, ok && state.AdminID == adminID && state.Step == step
}

func parseGuidedRoutingValue(field, text string) (any, error) {
	text = strings.TrimSpace(text)
	if text == "-" {
		return nil, nil
	}
	if text == "" {
		return nil, fmt.Errorf("a value is required")
	}
	switch field {
	case "ruleTag", "port", "sourcePort", "localPort", "network", "vlessRoute":
		if strings.ContainsAny(text, "\r\n") {
			return nil, fmt.Errorf("enter a single value")
		}
		if err := validateRoutingField(field, text); err != nil {
			return nil, err
		}
		return text, nil
	default:
		lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
		values := make([]any, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				values = append(values, line)
			}
		}
		if err := validateRoutingField(field, values); err != nil {
			return nil, err
		}
		return values, nil
	}
}

func (s *BotService) startGuidedRoutingInput(cb *tgCallback, ref routingRuleRef, field string, newRule bool) {
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), routingPageBackMarkup(s.cfg.Language))
		return
	}
	if !routingEditableFields[field] {
		return
	}
	var current any
	if !newRule {
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		rule, err := lookupRoutingRule(root, ref)
		if err != nil || protectedRoutingRule(root, rule) {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		current = rule[field]
	}
	clearInputSessions(chatID)
	step := "edit"
	if newRule {
		step = "new-input"
	}
	setGuidedRoutingWizard(chatID, guidedRoutingWizard{AdminID: cb.From.ID, Step: step, Field: field, Ref: ref, Expires: time.Now().Add(5 * time.Minute)})
	values := strings.Join(routingRuleValueLines(current), "\n")
	if current == nil {
		values = tr(s.cfg.Language, "routing.none")
	}
	if len(values) > 800 {
		values = truncateLabel(values, 800) + " …"
	}
	prompt := trh(s.cfg.Language, "routing.guide.input", routingFieldLabel(s.cfg.Language, field), values)
	if field == "domain" || field == "domains" {
		prompt += "\n" + trh(s.cfg.Language, "routing.guide.domain_example")
	}
	if field == "ip" {
		prompt += "\n" + trh(s.cfg.Language, "routing.guide.ip_example")
	}
	if field == "port" {
		prompt += "\n" + trh(s.cfg.Language, "routing.guide.port_example")
	}
	back := "rt:add"
	if !newRule {
		back = routingRefData("v", ref)
	}
	markup := newKeyboard(newRow(newButton(tr(s.cfg.Language, "common.cancel"), back)))
	s.sendOrEdit(chatID, msgID, prompt, markup)
}

func (s *BotService) queueGuidedRoutingChange(chatID, adminID int64, msgID int, pending pendingRoutingChange, body htmlText) {
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "node.tokenerror"))
		return
	}
	key := hex.EncodeToString(token[:])
	pending.ChatID = chatID
	pending.AdminID = adminID
	pending.Expires = time.Now().Add(3 * time.Minute)
	pendingRoutingChanges.Lock()
	for k, value := range pendingRoutingChanges.items {
		if time.Now().After(value.Expires) {
			delete(pendingRoutingChanges.items, k)
		}
	}
	pendingRoutingChanges.items[key] = pending
	pendingRoutingChanges.Unlock()
	markup := newKeyboard(newRow(newButton(tr(s.cfg.Language, "common.confirm"), "routing_confirm:"+key), newButton(tr(s.cfg.Language, "common.cancel"), "routing_cancel:"+key)))
	s.sendOrEdit(chatID, msgID, body, markup)
}

func previewRoutingValue(value any) string {
	if value == nil {
		return "—"
	}
	text := strings.Join(routingRuleValueLines(value), "\n")
	if len(text) > 800 {
		return truncateLabel(text, 790) + " …"
	}
	return text
}

func (s *BotService) offerGuidedRoutingRuleChange(chatID, adminID int64, msgID int, change routingRuleEdit) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), routingPageBackMarkup(s.cfg.Language))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var before any
	if err == nil {
		if change.Kind == "move" {
			change.ExpectedListETag, err = routingListETag(root)
		}
		rule, e := lookupRoutingRule(root, change.Ref)
		if errors.Is(e, errRoutingRuleChanged) {
			clearGuidedRoutingWizard(chatID)
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		if e != nil {
			err = e
		} else if change.Kind == "field" {
			before = rule[change.Field]
		} else if change.Kind == "target" {
			before = routingRuleTarget(rule)
		}
	}
	if err == nil {
		_, err = applyRoutingRuleEdit(root, change)
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), routingPageBackMarkup(s.cfg.Language))
		return
	}
	var body htmlText
	switch change.Kind {
	case "field":
		body = trh(s.cfg.Language, "routing.guide.confirm_field", change.Ref.Index, routingFieldLabel(s.cfg.Language, change.Field), previewRoutingValue(before), previewRoutingValue(change.Value))
	case "target":
		body = trh(s.cfg.Language, "routing.guide.confirm_target", change.Ref.Index, previewRoutingValue(before), previewRoutingValue(change.Value))
	case "move":
		direction := tr(s.cfg.Language, "routing.guide.up")
		if change.Direction > 0 {
			direction = tr(s.cfg.Language, "routing.guide.down")
		}
		body = trh(s.cfg.Language, "routing.guide.confirm_move", change.Ref.Index, direction)
	case "delete":
		body = trh(s.cfg.Language, "routing.guide.confirm_delete", change.Ref.Index)
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{RuleEdit: &change}, body)
}

func (s *BotService) handleGuidedRoutingInput(msg *tgMessage) bool {
	if msg.Chat == nil || msg.From == nil {
		return false
	}
	chatID := msg.Chat.ID
	state, ok := getGuidedRoutingWizard(chatID, msg.From.ID, "edit")
	if !ok {
		state, ok = getGuidedRoutingWizard(chatID, msg.From.ID, "new-input")
	}
	if !ok {
		return false
	}
	if msg.Command() == "cancel" {
		clearGuidedRoutingWizard(chatID)
		s.sendRoutingPage(chatID, 0, 0, tr(s.cfg.Language, "wizard.cancelled"))
		return true
	}
	if msg.Command() != "" {
		return false
	}
	if !s.cfg.AllowMutations {
		clearGuidedRoutingWizard(chatID)
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	value, err := parseGuidedRoutingValue(state.Field, msg.Text)
	if err != nil || (state.Step == "new-input" && value == nil) {
		if err == nil {
			err = fmt.Errorf("a new rule needs a condition")
		}
		s.sendAlert(chatID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		return true
	}
	if state.Step == "edit" {
		root, e := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		change := routingRuleEdit{Ref: state.Ref, Kind: "field", Field: state.Field, Value: value}
		if e == nil {
			_, e = applyRoutingRuleEdit(root, change)
		}
		if errors.Is(e, errRoutingRuleChanged) {
			clearGuidedRoutingWizard(chatID)
			s.sendRoutingPage(chatID, 0, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return true
		}
		if e != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "routing.guide.retry", e.Error()))
			return true
		}
		clearGuidedRoutingWizard(chatID)
		s.offerGuidedRoutingRuleChange(chatID, msg.From.ID, 0, change)
		return true
	}
	var id [6]byte
	if _, err = rand.Read(id[:]); err != nil {
		clearGuidedRoutingWizard(chatID)
		s.sendAlert(chatID, trh(s.cfg.Language, "node.tokenerror"))
		return true
	}
	state.Rule = map[string]any{"type": "field", "ruleTag": "route-" + hex.EncodeToString(id[:]), state.Field: value}
	state.Step = "new-target"
	state.Expires = time.Now().Add(5 * time.Minute)
	setGuidedRoutingWizard(chatID, state)
	cb := &tgCallback{From: msg.From, Message: &tgMessage{Chat: msg.Chat, MessageID: 0}}
	s.sendRoutingTargets(cb, nil, 0)
	return true
}

func (s *BotService) handleGuidedRoutingCallback(cb *tgCallback) {
	if cb.Message == nil || cb.From == nil {
		return
	}
	parts := strings.Split(cb.Data, ":")
	if len(parts) < 2 {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	action := parts[1]
	if (action == "test" || action == "testenable") && len(parts) == 2 {
		s.handleRouteTestCallback(cb)
		return
	}
	if action == "page" && len(parts) == 3 {
		offset, err := strconv.Atoi(parts[2])
		if err == nil {
			clearInputSessions(chatID)
			s.sendRoutingPage(chatID, msgID, offset, "")
		}
		return
	}
	if action == "advanced" && len(parts) == 2 {
		clearInputSessions(chatID)
		s.sendRoutingAdvanced(chatID, msgID)
		return
	}
	if action == "add" && len(parts) == 2 {
		clearInputSessions(chatID)
		s.sendRoutingAddPicker(chatID, msgID)
		return
	}
	if action == "nf" && len(parts) == 3 {
		switch parts[2] {
		case "domain", "ip", "port", "inboundTag":
			s.startGuidedRoutingInput(cb, routingRuleRef{}, parts[2], true)
		}
		return
	}
	if action == "nt" && len(parts) == 3 {
		if _, ok := getGuidedRoutingWizard(chatID, cb.From.ID, "new-target"); !ok {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "wizard.expired"))
			return
		}
		offset, err := strconv.Atoi(parts[2])
		if err == nil {
			s.sendRoutingTargets(cb, nil, offset)
		}
		return
	}
	if action == "np" && len(parts) == 3 {
		state, ok := getGuidedRoutingWizard(chatID, cb.From.ID, "new-target")
		if !ok || !s.cfg.AllowMutations {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "wizard.expired"))
			return
		}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		target, ok := findRoutingTarget(root, parts[2])
		if !ok {
			s.sendRoutingTargets(cb, nil, 0)
			return
		}
		state.Rule[target.Kind] = target.Tag
		state.Step = "new-position"
		state.Expires = time.Now().Add(5 * time.Minute)
		setGuidedRoutingWizard(chatID, state)
		rows := [][]tgButton{
			newRow(newButton(tr(s.cfg.Language, "routing.guide.first"), "rt:npos:first"), newButton(tr(s.cfg.Language, "routing.guide.last"), "rt:npos:last")),
			newRow(newButton(tr(s.cfg.Language, "common.cancel"), "rt:page:0"))}
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.guide.position"), newKeyboard(rows...))
		return
	}
	if action == "npos" && len(parts) == 3 {
		state, ok := getGuidedRoutingWizard(chatID, cb.From.ID, "new-position")
		if !ok || !s.cfg.AllowMutations {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "wizard.expired"))
			return
		}
		if parts[2] != "first" && parts[2] != "last" {
			return
		}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		etag, err := routingListETag(root)
		if err != nil {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		name, _ := state.Rule["ruleTag"].(string)
		change := routingChange{Kind: "add", RuleTag: name, Rule: state.Rule}
		position := tr(s.cfg.Language, "routing.guide.last")
		if parts[2] == "first" {
			change.Position = 1
			if routingHasInternalInbound(root) {
				change.Position = 2
			}
			position = tr(s.cfg.Language, "routing.guide.first")
		}
		if _, err = applyGuidedRoutingAdd(root, change); err != nil {
			s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), routingPageBackMarkup(s.cfg.Language))
			return
		}
		clearGuidedRoutingWizard(chatID)
		body := trh(s.cfg.Language, "routing.guide.confirm_add", routingFieldLabel(s.cfg.Language, state.Field), previewRoutingValue(state.Rule[state.Field]), routingRuleTarget(state.Rule), position)
		s.queueGuidedRoutingChange(chatID, cb.From.ID, msgID, pendingRoutingChange{Change: change, ExpectedRules: etag}, body)
		return
	}
	if action == "f" && len(parts) == 5 {
		ref, err := parseRoutingRef(parts[3], parts[4])
		if err == nil {
			s.startGuidedRoutingInput(cb, ref, parts[2], false)
		}
		return
	}
	if action == "t" && (len(parts) == 4 || len(parts) == 5) {
		ref, err := parseRoutingRef(parts[2], parts[3])
		if err != nil {
			return
		}
		offset := 0
		if len(parts) == 5 {
			offset, err = strconv.Atoi(parts[4])
			if err != nil {
				return
			}
		}
		s.sendRoutingTargets(cb, &ref, offset)
		return
	}
	if action == "p" && len(parts) == 5 {
		ref, err := parseRoutingRef(parts[2], parts[3])
		if err != nil {
			return
		}
		root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		target, ok := findRoutingTarget(root, parts[4])
		if !ok {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		s.offerGuidedRoutingRuleChange(chatID, cb.From.ID, msgID, routingRuleEdit{Ref: ref, Kind: "target", Field: target.Kind, Value: target.Tag})
		return
	}
	if len(parts) != 4 {
		return
	}
	ref, err := parseRoutingRef(parts[2], parts[3])
	if err != nil {
		return
	}
	switch action {
	case "v":
		clearInputSessions(chatID)
		s.sendRoutingRuleDetail(chatID, msgID, ref)
	case "more":
		s.sendRoutingMoreFields(chatID, msgID, ref)
	case "up":
		s.offerGuidedRoutingRuleChange(chatID, cb.From.ID, msgID, routingRuleEdit{Ref: ref, Kind: "move", Direction: -1})
	case "down":
		s.offerGuidedRoutingRuleChange(chatID, cb.From.ID, msgID, routingRuleEdit{Ref: ref, Kind: "move", Direction: 1})
	case "del":
		s.offerGuidedRoutingRuleChange(chatID, cb.From.ID, msgID, routingRuleEdit{Ref: ref, Kind: "delete"})
	}
}
