package main

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

func parseRouteTestInput(raw []byte, jsonOnly bool) (routeTestInput, error) {
	if len(raw) > telegramJSONDocumentLimit {
		return routeTestInput{}, errors.New("input exceeds 1 MiB")
	}
	var input routeTestInput
	value := strings.TrimSpace(string(raw))
	if jsonOnly || strings.HasPrefix(value, "{") {
		if err := decodeAgentInput(raw, &input); err != nil {
			return input, errors.New("expected one JSON route context with documented fields")
		}
	} else {
		parts := strings.Fields(value)
		if len(parts) < 1 || len(parts) > 2 || len(value) > 1024 {
			return input, errors.New("send a domain/IP with optional port, or tcp/udp followed by that target")
		}
		host := parts[len(parts)-1]
		if len(parts) == 2 {
			input.Network = strings.ToLower(parts[0])
		}
		bareIP := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if ip, err := netip.ParseAddr(bareIP); err == nil {
			input.IP = ip.String()
		} else {
			if parsed, port, err := net.SplitHostPort(host); err == nil {
				host = parsed
				input.Port, err = strconv.Atoi(port)
				if err != nil || input.Port < 1 || input.Port > 65535 {
					return input, errors.New("target port must be between 1 and 65535")
				}
			} else if strings.ContainsAny(host, ":[]") {
				return input, errors.New("use host:port or [IPv6]:port")
			}
			if ip, err := netip.ParseAddr(host); err == nil {
				input.IP = ip.String()
			} else {
				input.Domain = host
			}
		}
	}
	_, _, _, err := buildRouteTestContext(input)
	return input, err
}

func routeTestBackMarkup(lang string) tgKeyboard {
	return newKeyboard(newRow(
		newButton(tr(lang, "routing.guide.back"), "rt:page:0"),
		newButton(tr(lang, "menu.home"), "menu_main")))
}

func (s *BotService) sendRouteTestPage(chatID, adminID int64, msgID int, notice string) {
	clearOtherRoutingInputs(chatID)
	body := trh(s.cfg.Language, "route.test.title")
	if notice != "" {
		body += "\n" + esc(notice) + "\n"
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	if err == nil {
		_, err = routingAPIAddress(s.cfg, root)
	}
	markup := routeTestBackMarkup(s.cfg.Language)
	if errors.Is(err, errRoutingServiceDisabled) {
		body += trh(s.cfg.Language, "route.test.disabled")
		if s.cfg.AllowMutations {
			markup.InlineKeyboard = append([][]tgButton{newRow(
				newButton(tr(s.cfg.Language, "balancer.live.enable"), "rt:testenable"))}, markup.InlineKeyboard...)
		}
	} else if err != nil {
		body += trh(s.cfg.Language, "route.test.failed", err.Error())
	} else {
		routingWizards.Lock()
		routingWizards.items[chatID] = routingWizard{AdminID: adminID, Kind: "route_test", Expires: time.Now().Add(10 * time.Minute)}
		routingWizards.Unlock()
		body += trh(s.cfg.Language, "route.test.prompt")
	}
	s.sendOrEdit(chatID, msgID, body, markup)
}

func (s *BotService) handleRouteTestCallback(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	if cb.Data == "rt:test" {
		s.sendRouteTestPage(chatID, cb.From.ID, msgID, "")
		return
	}
	if cb.Data != "rt:testenable" {
		return
	}
	if !s.cfg.AllowMutations {
		s.sendRouteTestPage(chatID, cb.From.ID, msgID, tr(s.cfg.Language, "manage.writesdisabled"))
		return
	}
	root, err := decodeBindingMigrationConfig(s.cfg.XrayConfigPath)
	changed := false
	if err == nil {
		server := s.cfg.StatsAPIServer
		if server == "" {
			server = "127.0.0.1:10085"
		}
		changed, err = applyRoutingService(root, server)
	}
	if err != nil {
		s.sendRouteTestPage(chatID, cb.From.ID, msgID, err.Error())
		return
	}
	if !changed {
		s.sendRouteTestPage(chatID, cb.From.ID, msgID, "")
		return
	}
	clearOtherRoutingInputs(chatID)
	s.queueGuidedRoutingChange(chatID, cb.From.ID, msgID, pendingRoutingChange{EnableRoutingAPI: true, ReturnRouteTest: true}, trh(s.cfg.Language, "route.test.enable_confirm"))
}

func renewRouteTestWizard(chatID int64, expected routingWizard) (routingWizard, bool) {
	routingWizards.Lock()
	defer routingWizards.Unlock()
	current, ok := routingWizards.items[chatID]
	if !ok || current != expected || time.Now().After(current.Expires) {
		return routingWizard{}, false
	}
	current.Expires = time.Now().Add(10 * time.Minute)
	routingWizards.items[chatID] = current
	return current, true
}

func routeTestWizardCurrent(chatID int64, expected routingWizard) bool {
	routingWizards.Lock()
	defer routingWizards.Unlock()
	current, ok := routingWizards.items[chatID]
	return ok && current == expected && time.Now().Before(current.Expires)
}

func (s *BotService) handleRouteTestInput(msg *tgMessage, state routingWizard) bool {
	if msg.Command() == "cancel" {
		if consumeRoutingWizard(msg.Chat.ID, state) {
			s.sendRoutingPage(msg.Chat.ID, 0, 0, tr(s.cfg.Language, "wizard.cancelled"))
		}
		return true
	}
	if msg.Command() != "" {
		return false
	}
	raw := []byte(msg.Text)
	if msg.Document != nil {
		var err error
		raw, err = readTelegramJSONDocument(s.tg, msg.Document)
		if err != nil {
			if routeTestWizardCurrent(msg.Chat.ID, state) {
				s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
			}
			return true
		}
	}
	input, err := parseRouteTestInput(raw, msg.Document != nil)
	if err != nil {
		if routeTestWizardCurrent(msg.Chat.ID, state) {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "routing.guide.retry", err.Error()))
		}
		return true
	}
	active, ok := renewRouteTestWizard(msg.Chat.ID, state)
	if !ok {
		return true
	}
	result, err := queryRouteTest(s.cfg, input)
	if !routeTestWizardCurrent(msg.Chat.ID, active) {
		return true
	}
	body := trh(s.cfg.Language, "route.test.title")
	if err != nil {
		body += trh(s.cfg.Language, "route.test.failed", err.Error())
	} else {
		body += trh(s.cfg.Language, "route.test.destination", result.Target, result.Network)
		if result.Matched {
			body += trh(s.cfg.Language, "route.test.matched", result.OutboundTag)
			if len(result.GroupTags) > 0 {
				body += trh(s.cfg.Language, "route.test.groups", strings.Join(result.GroupTags, ", "))
			}
		} else {
			body += trh(s.cfg.Language, "route.test.no_match")
			if result.SavedDefaultOutbound != nil {
				tag := result.SavedDefaultOutbound.Tag
				if tag == "" {
					tag = tr(s.cfg.Language, "routing.none")
				}
				body += trh(s.cfg.Language, "route.test.saved_default", tag)
			}
		}
		body += trh(s.cfg.Language, "route.test.checked", result.CheckedAt.Format("2006-01-02 15:04:05 UTC"))
	}
	body += trh(s.cfg.Language, "route.test.continue")
	s.sendOrEdit(msg.Chat.ID, 0, body, routeTestBackMarkup(s.cfg.Language))
	return true
}
