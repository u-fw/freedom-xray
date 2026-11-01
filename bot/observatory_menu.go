package main

import (
	"errors"
	"strings"
	"time"
)

func observatoryShort(kind string) string {
	if kind == "observatory" {
		return "o"
	}
	if kind == "burstObservatory" {
		return "b"
	}
	return ""
}

func observatoryKind(short string) string {
	if short == "o" {
		return "observatory"
	}
	if short == "b" {
		return "burstObservatory"
	}
	return ""
}

func observatoryRef(record observatoryRecord) string {
	if !record.Configured {
		return "new"
	}
	return record.ETag
}

func observatoryCallback(action, kind, ref string) string {
	return "mo:" + action + ":" + observatoryShort(kind) + ":" + ref
}

func observatoryBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(
		newButton(tr(lang, "monitor.back"), "mo:page"),
		newButton(tr(lang, "balancer.back"), "bl:page:0")))
}

func (s *BotService) sendObservatoryPage(chatID int64, msgID int, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), balancerBackMarkup(s.cfg.Language))
		return
	}
	records, err := observatoryRecords(root, false)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.error", err.Error()), balancerBackMarkup(s.cfg.Language))
		return
	}
	body := trh(s.cfg.Language, "monitor.list")
	if notice != "" {
		body += "\n" + esc(notice) + "\n"
	}
	var rows [][]tgButton
	for _, record := range records {
		state := tr(s.cfg.Language, "monitor.notconfigured")
		if record.Configured {
			state = tr(s.cfg.Language, "monitor.configured")
		}
		rows = append(rows, newRow(newButton(record.Kind+" · "+state, observatoryCallback("v", record.Kind, observatoryRef(record)))))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "balancer.back"), "bl:page:0")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func currentObservatoryRecord(root map[string]any, kind, ref string) (observatoryRecord, error) {
	records, err := observatoryRecords(root, false)
	if err != nil {
		return observatoryRecord{}, err
	}
	for _, record := range records {
		if record.Kind == kind && observatoryRef(record) == ref {
			return record, nil
		}
	}
	return observatoryRecord{}, errObservatoryChanged
}

func (s *BotService) sendObservatoryDetail(chatID int64, msgID int, kind, ref string) {
	s.sendObservatoryDetailNotice(chatID, msgID, kind, ref, "")
}

func (s *BotService) returnObservatoryDetail(chatID int64, msgID int, kind, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		records, err := observatoryRecords(root, false)
		if err == nil {
			for _, record := range records {
				if record.Kind == kind {
					s.sendObservatoryDetailNotice(chatID, msgID, kind, observatoryRef(record), notice)
					return
				}
			}
		}
	}
	s.sendObservatoryPage(chatID, msgID, tr(s.cfg.Language, "monitor.changed"))
}

func (s *BotService) sendObservatoryDetailNotice(chatID int64, msgID int, kind, ref, notice string) {
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendObservatoryPage(chatID, msgID, tr(s.cfg.Language, "monitor.changed"))
		return
	}
	record, err := currentObservatoryRecord(root, kind, ref)
	if err != nil {
		s.sendObservatoryPage(chatID, msgID, tr(s.cfg.Language, "monitor.changed"))
		return
	}
	body := trh(s.cfg.Language, "monitor.detail", kind)
	if notice != "" {
		body = esc(notice) + "\n\n" + body
	}
	if record.Configured {
		body += trh(s.cfg.Language, "monitor.selectors", strings.Join(stringList(record.Config["subjectSelector"]), "\n"))
		config, _ := readObservatory(root, kind)
		interval, _ := config["probeInterval"].(string)
		if kind == "burstObservatory" {
			ping, _ := getMap(config["pingConfig"])
			interval, _ = ping["interval"].(string)
		}
		if interval == "" {
			interval = tr(s.cfg.Language, "node.default")
		}
		body += trh(s.cfg.Language, "monitor.interval", interval)
		if len(record.RedactedFields) > 0 {
			body += trh(s.cfg.Language, "monitor.hidden")
		}
	} else {
		body += trh(s.cfg.Language, "monitor.notconfigured_detail")
	}
	var rows [][]tgButton
	if s.cfg.AllowMutations {
		label := tr(s.cfg.Language, "monitor.edit")
		if !record.Configured {
			label = tr(s.cfg.Language, "monitor.create")
		}
		rows = append(rows, newRow(newButton(label, observatoryCallback("edit", kind, ref))))
		if record.Configured {
			rows = append(rows, newRow(
				newButton(tr(s.cfg.Language, "monitor.edit_interval"), observatoryCallback("interval", kind, ref)),
				newButton(tr(s.cfg.Language, "monitor.edit_url"), observatoryCallback("url", kind, ref))))
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "monitor.delete"), observatoryCallback("delete", kind, ref))))
		}
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "monitor.native.edit"), observatoryCallback("json", kind, ref))))
	}
	if record.Configured && s.cfg.AllowSensitiveBackup {
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "config.export.button"), nativeConfigExportCallback("monitor", observatoryIndex(kind), record.ETag))))
	}
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "monitor.back"), "mo:page"), newButton(tr(s.cfg.Language, "balancer.back"), "bl:page:0")))
	s.sendOrEdit(chatID, msgID, body, newKeyboard(rows...))
}

func (s *BotService) offerObservatoryChange(chatID, adminID int64, msgID int, change observatoryChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), observatoryBackMarkup(s.cfg.Language))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = applyObservatoryChange(root, change)
	}
	if errors.Is(err, errObservatoryChanged) {
		s.sendObservatoryPage(chatID, msgID, tr(s.cfg.Language, "monitor.changed"))
		return
	}
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "routing.edit.invalid", err.Error()), observatoryBackMarkup(s.cfg.Language))
		return
	}
	detail := trh(s.cfg.Language, "monitor.confirm.delete", change.Kind)
	if change.Action == "field" {
		value := change.Value
		field := tr(s.cfg.Language, "monitor.edit_"+change.Field)
		if change.Field == "url" {
			value = tr(s.cfg.Language, "monitor.hidden_value")
		}
		detail = trh(s.cfg.Language, "monitor.confirm.field", change.Kind, field, value)
	} else if change.Action != "delete" {
		selectors := change.Selectors
		if change.Action == "put" {
			selectors = change.Config["subjectSelector"]
		}
		key := "monitor.confirm.put"
		if change.Action == "put" && change.ETag != "" {
			key = "monitor.native.confirm_replace"
		}
		detail = trh(s.cfg.Language, key, change.Kind, strings.Join(stringList(selectors), ", "))
	}
	s.queueGuidedRoutingChange(chatID, adminID, msgID, pendingRoutingChange{Observatory: &change}, detail)
}

func (s *BotService) handleObservatoryCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	parts := strings.Split(cb.Data, ":")
	if len(parts) == 2 && parts[1] == "page" {
		clearInputSessions(chatID)
		s.sendObservatoryPage(chatID, msgID, "")
		return
	}
	if len(parts) != 4 {
		return
	}
	kind, ref := observatoryKind(parts[2]), parts[3]
	if kind == "" {
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendObservatoryPage(chatID, msgID, tr(s.cfg.Language, "monitor.changed"))
		return
	}
	_, err = currentObservatoryRecord(root, kind, ref)
	if err != nil {
		s.sendObservatoryPage(chatID, msgID, tr(s.cfg.Language, "monitor.changed"))
		return
	}
	if parts[1] == "v" {
		clearInputSessions(chatID)
		s.sendObservatoryDetail(chatID, msgID, kind, ref)
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), observatoryBackMarkup(s.cfg.Language))
		return
	}
	switch parts[1] {
	case "edit", "interval", "url", "json":
		if parts[1] != "edit" && parts[1] != "json" && ref == "new" {
			return
		}
		clearInputSessions(chatID)
		etag := ref
		if etag == "new" {
			etag = ""
		}
		step, prompt := "monitor-selectors", "monitor.selector_prompt"
		if parts[1] == "interval" {
			step, prompt = "monitor-interval", "monitor.interval_prompt"
		}
		if parts[1] == "url" {
			step, prompt = "monitor-url", "monitor.url_prompt"
		}
		if parts[1] == "json" {
			step, prompt = "monitor-native", "monitor.native.prompt"
		}
		setBalancerWizard(chatID, balancerWizard{AdminID: cb.From.ID, Step: step, Tag: kind, ETag: etag, Expires: time.Now().Add(5 * time.Minute)})
		var text htmlText
		if parts[1] == "json" {
			example := `{"subjectSelector":["proxy-"],"probeInterval":"1m","enableConcurrency":true}`
			if kind == "burstObservatory" {
				example = `{"subjectSelector":["proxy-"],"pingConfig":{"interval":"1m","sampling":4,"timeout":"5s"}}`
			}
			text = trh(s.cfg.Language, prompt, kind, example)
		} else {
			text = trh(s.cfg.Language, prompt)
		}
		s.sendOrEdit(chatID, msgID, text, observatoryBackMarkup(s.cfg.Language))
	case "delete":
		if ref == "new" {
			return
		}
		s.offerObservatoryChange(chatID, cb.From.ID, msgID, observatoryChange{Kind: kind, Action: "delete", ETag: ref})
	}
}
