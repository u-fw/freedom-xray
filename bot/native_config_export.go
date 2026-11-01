package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type nativeConfigExport struct {
	ChatID  int64
	AdminID int64
	Kind    string
	Index   int
	ETag    string
	Expires time.Time
}

var pendingNativeConfigExports = struct {
	sync.Mutex
	items map[string]nativeConfigExport
}{items: map[string]nativeConfigExport{}}

func nativeConfigExportCallback(kind string, index int, etag string) string {
	return fmt.Sprintf("ce:%s:%d:%s", kind, index, etag)
}

func nativeConfigExportObject(root map[string]any, kind string, index int, etag string) (any, error) {
	switch kind {
	case "inbound":
		return lookupNativeInbound(root, index, etag)
	case "outbound":
		return lookupOutbound(root, index, etag)
	case "rule":
		return lookupRoutingRule(root, routingRuleRef{Index: index, ETag: etag})
	case "balancer":
		records, err := balancerRecords(root)
		if err != nil {
			return nil, err
		}
		if index < 1 || index > len(records) || records[index-1].ETag != etag {
			return nil, errBalancerChanged
		}
		return records[index-1].Balancer, nil
	case "monitor":
		return lookupObservatory(root, observatoryKindAtIndex(index), etag)
	case "section":
		if index < 1 || index > len(xraySectionNames) {
			return nil, errors.New("unknown section")
		}
		value, present, err := lookupXraySection(root, xraySectionNames[index-1], etag)
		if err == nil && !present {
			err = errors.New("section is not configured")
		}
		return value, err
	case "dns":
		actual, err := dnsETag(root)
		if err != nil {
			return nil, err
		}
		if index != 0 || len(etag) != 24 || actual != etag {
			return nil, errDNSChanged
		}
		object, err := readDNS(root)
		if err == nil && object == nil {
			err = errors.New("DNS is not configured")
		}
		return object, err
	default:
		return nil, errors.New("unknown export target")
	}
}

func (s *BotService) returnNativeConfigExport(chatID int64, msgID int, pending nativeConfigExport, changed bool) {
	switch pending.Kind {
	case "balancer":
		if !changed {
			root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
			if err == nil {
				object, err := nativeConfigExportObject(root, pending.Kind, pending.Index, pending.ETag)
				if err == nil {
					item, _ := getMap(object)
					tag, _ := item["tag"].(string)
					s.sendBalancerDetail(chatID, msgID, userCallbackKey(tag), pending.ETag)
					return
				}
			}
		}
		s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
	case "monitor":
		kind := observatoryKindAtIndex(pending.Index)
		if changed || kind == "" {
			s.sendObservatoryPage(chatID, msgID, tr(s.cfg.Language, "monitor.changed"))
		} else {
			s.sendObservatoryDetail(chatID, msgID, kind, pending.ETag)
		}
	case "section":
		if changed || pending.Index < 1 || pending.Index > len(xraySectionNames) {
			s.sendXraySectionsPage(chatID, msgID, tr(s.cfg.Language, "xray.section.changed"))
		} else {
			s.sendXraySectionDetail(chatID, msgID, xraySectionNames[pending.Index-1], pending.ETag, "")
		}
	case "outbound":
		if changed {
			s.sendOutboundPage(chatID, msgID, 0, tr(s.cfg.Language, "outbound.changed"))
		} else {
			s.sendOutboundDetail(chatID, msgID, pending.Index, pending.ETag)
		}
	case "dns":
		notice := ""
		if changed {
			notice = tr(s.cfg.Language, "dns.changed")
		}
		s.sendDNSPage(chatID, msgID, 0, notice)
	case "rule":
		if changed {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
		} else {
			s.sendRoutingRuleDetail(chatID, msgID, routingRuleRef{Index: pending.Index, ETag: pending.ETag})
		}
	default:
		if changed {
			s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
		} else {
			s.sendNativeInboundDetail(chatID, msgID, pending.Index, pending.ETag)
		}
	}
}

func (s *BotService) handleNativeConfigExportCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	parts := strings.Split(cb.Data, ":")
	if len(parts) != 4 {
		return
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 {
		return
	}
	clearRoutingWizard(cb.Message.Chat.ID)
	s.askNativeConfigExport(cb, parts[1], index, parts[3])
}

func (s *BotService) askNativeConfigExport(cb *tgCallback, kind string, index int, etag string) {
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	if !s.cfg.AllowSensitiveBackup {
		s.sendAlert(chatID, trh(s.cfg.Language, "backup.disabled"))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = nativeConfigExportObject(root, kind, index, etag)
	}
	if err != nil {
		s.returnNativeConfigExport(chatID, msgID, nativeConfigExport{Kind: kind, Index: index, ETag: etag}, true)
		return
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "node.tokenerror"))
		return
	}
	key := hex.EncodeToString(token[:])
	pendingNativeConfigExports.Lock()
	for k, pending := range pendingNativeConfigExports.items {
		if time.Now().After(pending.Expires) {
			delete(pendingNativeConfigExports.items, k)
		}
	}
	pendingNativeConfigExports.items[key] = nativeConfigExport{ChatID: chatID, AdminID: cb.From.ID, Kind: kind, Index: index, ETag: etag, Expires: time.Now().Add(3 * time.Minute)}
	pendingNativeConfigExports.Unlock()
	markup := newKeyboard(newRow(
		newButton(tr(s.cfg.Language, "common.confirm"), "ni:exconfirm:"+key),
		newButton(tr(s.cfg.Language, "common.cancel"), "ni:excancel:"+key)))
	var label string
	if kind == "dns" {
		label = tr(s.cfg.Language, "config.export.kind.dns")
	} else if kind == "section" {
		label = tr(s.cfg.Language, "xray.section."+xraySectionNames[index-1])
	} else if kind == "monitor" {
		label = observatoryKindAtIndex(index)
	} else {
		label = tr(s.cfg.Language, "config.export.kind."+kind, index)
	}
	s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "config.export.confirm", label), markup)
}

func (s *BotService) resolveNativeConfigExport(cb *tgCallback, key string, confirm bool) {
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	pendingNativeConfigExports.Lock()
	pending, ok := pendingNativeConfigExports.items[key]
	if ok && pending.ChatID == chatID && pending.AdminID == cb.From.ID {
		delete(pendingNativeConfigExports.items, key)
	}
	pendingNativeConfigExports.Unlock()
	if ok && (pending.ChatID != chatID || pending.AdminID != cb.From.ID) {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || time.Now().After(pending.Expires) {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.expired"), s.backToMainMarkup())
		return
	}
	if !confirm {
		s.returnNativeConfigExport(chatID, msgID, pending, false)
		return
	}
	if !s.cfg.AllowSensitiveBackup {
		s.sendAlert(chatID, trh(s.cfg.Language, "backup.disabled"))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	var object any
	if err == nil {
		object, err = nativeConfigExportObject(root, pending.Kind, pending.Index, pending.ETag)
	}
	if err != nil {
		s.returnNativeConfigExport(chatID, msgID, pending, true)
		return
	}
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil || len(data)+1 > telegramJSONDocumentLimit {
		s.sendAlert(chatID, trh(s.cfg.Language, "config.export.failed"))
		return
	}
	if err := s.tg.sendDocument(chatID, "xray-"+pending.Kind+".json", append(data, '\n'), tr(s.cfg.Language, "config.export.caption")); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "config.export.failed"))
		return
	}
	s.returnNativeConfigExport(chatID, msgID, pending, false)
}
