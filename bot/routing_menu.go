package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type routingWizard struct {
	AdminID int64
	Kind    string
	Expires time.Time
}
type pendingRoutingChange struct {
	ChatID           int64
	AdminID          int64
	Change           routingChange
	Network          *networkChange
	Host             *string
	RuleEdit         *routingRuleEdit
	Balancer         *balancerChange
	Observatory      *observatoryChange
	Outbound         *outboundChange
	DNS              *dnsChange
	DNSHost          *dnsHostChange
	NativeInbound    *nativeInboundChange
	NativeClient     *nativeClientChange
	EnableRoutingAPI bool
	LiveBalancerTag  string
	LiveBalancerETag string
	ExpectedRules    string
	Expires          time.Time
}

var routingWizards = struct {
	sync.Mutex
	items map[int64]routingWizard
}{items: map[int64]routingWizard{}}
var pendingRoutingChanges = struct {
	sync.Mutex
	items map[string]pendingRoutingChange
}{items: map[string]pendingRoutingChange{}}

func clearRoutingWizard(chatID int64) {
	routingWizards.Lock()
	delete(routingWizards.items, chatID)
	routingWizards.Unlock()
	clearGuidedRoutingWizard(chatID)
	clearBalancerWizard(chatID)
	clearOutboundWizard(chatID)
	clearDNSWizard(chatID)
	clearNativeInboundWizard(chatID)
}

func (s *BotService) beginRoutingWizard(cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	kind := strings.TrimPrefix(cb.Data, "routing_manage:")
	if kind != "add" && kind != "update" && kind != "delete" && kind != "outbound_put" && kind != "outbound_delete" && kind != "dns_put" && kind != "server_host" {
		return
	}
	clearRoutingWizard(chatID)
	clearNodeCreateState(chatID)
	nodePathWizards.Lock()
	delete(nodePathWizards.items, chatID)
	nodePathWizards.Unlock()
	userLimitWizards.Lock()
	delete(userLimitWizards.items, chatID)
	userLimitWizards.Unlock()
	managementWizards.Lock()
	delete(managementWizards.states, chatID)
	managementWizards.Unlock()
	routingWizards.Lock()
	routingWizards.items[chatID] = routingWizard{AdminID: cb.From.ID, Kind: kind, Expires: time.Now().Add(5 * time.Minute)}
	routingWizards.Unlock()
	prompt := tr(s.cfg.Language, "routing.edit.ruleprompt")
	if kind == "delete" {
		prompt = tr(s.cfg.Language, "routing.edit.deleteprompt")
	} else if kind == "outbound_put" {
		prompt = tr(s.cfg.Language, "network.outbound.prompt")
	} else if kind == "outbound_delete" {
		prompt = tr(s.cfg.Language, "network.outbound.deleteprompt")
	} else if kind == "dns_put" {
		prompt = tr(s.cfg.Language, "network.dns.prompt")
	} else if kind == "server_host" {
		prompt = tr(s.cfg.Language, "serverhost.prompt")
	}
	s.sendOrEdit(chatID, msgID, prompt, s.backToMainMarkup())
}

func (s *BotService) handleRoutingInput(msg *tgbotapi.Message) bool {
	chatID := msg.Chat.ID
	routingWizards.Lock()
	state, ok := routingWizards.items[chatID]
	if !ok {
		routingWizards.Unlock()
		return false
	}
	if time.Now().After(state.Expires) {
		delete(routingWizards.items, chatID)
		routingWizards.Unlock()
		return false
	}
	if msg.From == nil || msg.From.ID != state.AdminID {
		routingWizards.Unlock()
		return false
	}
	if msg.Command() == "cancel" {
		delete(routingWizards.items, chatID)
		routingWizards.Unlock()
		s.sendAlert(chatID, tr(s.cfg.Language, "wizard.cancelled"))
		return true
	}
	if msg.Command() != "" {
		routingWizards.Unlock()
		return false
	}
	delete(routingWizards.items, chatID)
	routingWizards.Unlock()
	if !s.cfg.AllowMutations {
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.writesdisabled"))
		return true
	}
	change := routingChange{Kind: state.Kind}
	var network *networkChange
	var host *string
	if state.Kind == "server_host" {
		value := strings.TrimSpace(msg.Text)
		if value == "-" {
			value = ""
		}
		normalized, err := normalizeServerHost(value)
		if err != nil {
			s.sendAlert(chatID, tr(s.cfg.Language, "routing.edit.invalid", mdEsc(err.Error())))
			return true
		}
		host = &normalized
	} else if state.Kind == "outbound_delete" {
		network = &networkChange{Kind: "outbounds.delete", Tag: strings.TrimSpace(msg.Text)}
	} else if state.Kind == "outbound_put" || state.Kind == "dns_put" {
		value, err := decodeNetworkObject([]byte(msg.Text))
		if err != nil {
			s.sendAlert(chatID, tr(s.cfg.Language, "routing.edit.invalid", mdEsc(err.Error())))
			return true
		}
		if state.Kind == "outbound_put" {
			tag, _ := value["tag"].(string)
			network = &networkChange{Kind: "outbounds.put", Tag: tag, Value: value}
		} else {
			network = &networkChange{Kind: "dns.put", Value: value}
		}
	} else if state.Kind == "delete" {
		change.RuleTag = strings.TrimSpace(msg.Text)
	} else {
		rule, err := decodeRoutingRule([]byte(msg.Text))
		if err != nil {
			s.sendAlert(chatID, tr(s.cfg.Language, "routing.edit.invalid", err.Error()))
			return true
		}
		change.Rule = rule
		change.RuleTag, _ = rule["ruleTag"].(string)
	}
	var err error
	if host == nil {
		var root map[string]any
		root, err = decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
		if err == nil {
			if network != nil {
				_, err = applyNetworkChange(root, *network)
			} else {
				_, err = applyRoutingChange(root, change)
			}
		}
	}
	if err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "routing.edit.invalid", err.Error()))
		return true
	}
	var token [8]byte
	if _, err = rand.Read(token[:]); err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "node.tokenerror"))
		return true
	}
	key := hex.EncodeToString(token[:])
	pendingRoutingChanges.Lock()
	for k, value := range pendingRoutingChanges.items {
		if time.Now().After(value.Expires) {
			delete(pendingRoutingChanges.items, k)
		}
	}
	pendingRoutingChanges.items[key] = pendingRoutingChange{ChatID: chatID, AdminID: msg.From.ID, Change: change, Network: network, Host: host, Expires: time.Now().Add(3 * time.Minute)}
	pendingRoutingChanges.Unlock()
	markup := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.confirm"), "routing_confirm:"+key),
		tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language, "common.cancel"), "routing_cancel:"+key)))
	label, kind := change.RuleTag, change.Kind
	if network != nil {
		label, kind = network.Tag, network.Kind
		if label == "" {
			label = "DNS"
		}
	}
	if host != nil {
		label = *host
		if label == "" {
			label = "automatic"
		}
		s.sendOrEdit(chatID, 0, tr(s.cfg.Language, "serverhost.confirm", mdCode(label)), markup)
	} else {
		s.sendOrEdit(chatID, 0, tr(s.cfg.Language, "routing.edit.confirm", mdCode(label), mdCode(kind)), markup)
	}
	return true
}

func (s *BotService) resolveRoutingChange(cb *tgbotapi.CallbackQuery, confirm bool) {
	if cb.Message == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	key := strings.TrimPrefix(strings.TrimPrefix(cb.Data, "routing_confirm:"), "routing_cancel:")
	pendingRoutingChanges.Lock()
	pending, ok := pendingRoutingChanges.items[key]
	if ok && pending.ChatID == chatID && cb.From != nil && pending.AdminID == cb.From.ID {
		delete(pendingRoutingChanges.items, key)
	}
	pendingRoutingChanges.Unlock()
	if ok && (cb.From == nil || pending.ChatID != chatID || pending.AdminID != cb.From.ID) {
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || cb.From == nil || pending.ChatID != chatID || pending.AdminID != cb.From.ID || time.Now().After(pending.Expires) {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.expired"), s.backToMainMarkup())
		return
	}
	if !confirm {
		if pending.EnableRoutingAPI {
			s.returnToBalancerLive(chatID, msgID, pending.LiveBalancerTag, pending.LiveBalancerETag)
		} else if pending.NativeClient != nil {
			s.returnNativeClientList(chatID, msgID, pending.NativeClient.InboundIndex, "")
		} else if pending.NativeInbound != nil {
			s.sendNativeInboundPage(chatID, msgID, 0, "")
		} else if pending.DNSHost != nil {
			s.sendDNSHostsPage(chatID, msgID, 0, "")
		} else if pending.DNS != nil {
			s.sendDNSPage(chatID, msgID, 0, "")
		} else if pending.Outbound != nil {
			s.sendOutboundPage(chatID, msgID, 0, "")
		} else if pending.Observatory != nil {
			s.sendObservatoryPage(chatID, msgID, "")
		} else if pending.Balancer != nil {
			if pending.Balancer.ETag != "" {
				s.sendBalancerDetail(chatID, msgID, userCallbackKey(pending.Balancer.Tag), pending.Balancer.ETag)
			} else {
				s.sendBalancerPage(chatID, msgID, 0, "")
			}
		} else if pending.RuleEdit != nil {
			s.sendRoutingRuleDetail(chatID, msgID, pending.RuleEdit.Ref)
		} else if pending.ExpectedRules != "" {
			s.sendRoutingPage(chatID, msgID, 0, "")
		} else {
			s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.cancelled"), s.backToMainMarkup())
		}
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	var message string
	var err error
	if pending.Host != nil {
		var host string
		host, err = persistServerHost(s.cfg.ConfigFile, *pending.Host)
		if err == nil {
			if host == "" {
				host = "automatic"
			}
			message = tr(s.cfg.Language, "serverhost.done", mdEsc(host))
		}
	} else if pending.EnableRoutingAPI {
		message, err = enableRoutingService(s.cfg)
	} else if pending.NativeClient != nil {
		message, err = changeNativeClientConfig(s.cfg, *pending.NativeClient)
	} else if pending.NativeInbound != nil {
		message, err = changeNativeInboundConfig(s.cfg, *pending.NativeInbound)
	} else if pending.DNSHost != nil {
		message, err = changeDNSHostConfig(s.cfg, *pending.DNSHost)
	} else if pending.DNS != nil {
		message, err = changeDNSConfig(s.cfg, *pending.DNS)
	} else if pending.Outbound != nil {
		message, err = changeOutboundConfig(s.cfg, *pending.Outbound)
	} else if pending.Observatory != nil {
		message, err = changeObservatoryConfig(s.cfg, *pending.Observatory)
	} else if pending.Balancer != nil {
		message, err = changeBalancerConfig(s.cfg, *pending.Balancer)
	} else if pending.RuleEdit != nil {
		message, err = changeRoutingRule(s.cfg, *pending.RuleEdit)
	} else if pending.ExpectedRules != "" {
		message, err = commitManagedConfig(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, func(root map[string]any) (string, error) {
			current, e := routingListETag(root)
			if e != nil {
				return "", e
			}
			if current != pending.ExpectedRules {
				return "", errRoutingRuleChanged
			}
			return applyGuidedRoutingAdd(root, pending.Change)
		})
	} else if pending.Network != nil {
		message, err = changeNetworkConfig(s.cfg, *pending.Network)
	} else {
		message, err = changeRoutingConfig(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, pending.Change)
	}
	if err != nil {
		if pending.NativeClient != nil && (errors.Is(err, errNativeInboundChanged) || errors.Is(err, errRoutingRuleChanged)) {
			s.returnNativeClientList(chatID, msgID, pending.NativeClient.InboundIndex, tr(s.cfg.Language, "inbound.client.changed"))
			return
		}
		if errors.Is(err, errNativeInboundChanged) || (pending.NativeInbound != nil && errors.Is(err, errNodeDeletionChanged)) {
			s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.changed"))
			return
		}
		if errors.Is(err, errDNSChanged) {
			if pending.DNSHost != nil {
				s.sendDNSHostsPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
			} else {
				s.sendDNSPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.changed"))
			}
			return
		}
		if errors.Is(err, errOutboundChanged) {
			s.sendOutboundPage(chatID, msgID, 0, tr(s.cfg.Language, "outbound.changed"))
			return
		}
		if errors.Is(err, errObservatoryChanged) {
			s.sendObservatoryPage(chatID, msgID, tr(s.cfg.Language, "monitor.changed"))
			return
		}
		if errors.Is(err, errBalancerChanged) {
			s.sendBalancerPage(chatID, msgID, 0, tr(s.cfg.Language, "balancer.changed"))
			return
		}
		if errors.Is(err, errRoutingRuleChanged) {
			s.sendRoutingPage(chatID, msgID, 0, tr(s.cfg.Language, "routing.guide.changed"))
			return
		}
		markup := s.backToMainMarkup()
		if pending.EnableRoutingAPI {
			markup = balancerBackMarkup(s.cfg.Language)
		} else if pending.NativeClient != nil || pending.NativeInbound != nil {
			markup = nativeInboundBackMarkup(s.cfg.Language)
		} else if pending.DNSHost != nil {
			markup = dnsHostsBackMarkup(s.cfg.Language)
		} else if pending.DNS != nil {
			markup = dnsBackMarkup(s.cfg.Language)
		} else if pending.Outbound != nil {
			markup = outboundBackMarkup(s.cfg.Language)
		} else if pending.Observatory != nil {
			markup = observatoryBackMarkup(s.cfg.Language)
		} else if pending.Balancer != nil {
			markup = balancerBackMarkup(s.cfg.Language)
		} else if pending.RuleEdit != nil || pending.ExpectedRules != "" {
			markup = routingPageBackMarkup(s.cfg.Language)
		}
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "routing.edit.failed", mdEsc(err.Error())), markup)
		return
	}
	if pending.EnableRoutingAPI {
		s.returnToBalancerLive(chatID, msgID, pending.LiveBalancerTag, pending.LiveBalancerETag)
		return
	}
	if pending.NativeClient != nil {
		s.returnNativeClientList(chatID, msgID, pending.NativeClient.InboundIndex, tr(s.cfg.Language, "inbound.client.done"))
		return
	}
	if pending.NativeInbound != nil {
		s.sendNativeInboundPage(chatID, msgID, 0, tr(s.cfg.Language, "inbound.native.done"))
		return
	}
	if pending.DNSHost != nil {
		s.sendDNSHostsPage(chatID, msgID, 0, tr(s.cfg.Language, "dns.hosts.done"))
		return
	}
	markup := s.backToMainMarkup()
	if pending.DNS != nil {
		markup = dnsBackMarkup(s.cfg.Language)
	} else if pending.Outbound != nil {
		markup = outboundBackMarkup(s.cfg.Language)
	} else if pending.Observatory != nil {
		markup = observatoryBackMarkup(s.cfg.Language)
	} else if pending.Balancer != nil {
		markup = balancerBackMarkup(s.cfg.Language)
	} else if pending.RuleEdit != nil || pending.ExpectedRules != "" {
		markup = routingPageBackMarkup(s.cfg.Language)
	}
	s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "routing.edit.done", mdEsc(message)), markup)
}
