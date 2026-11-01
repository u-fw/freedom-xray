package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/u-fw/freedom-xray/bot/internal/xrayapi"
)

func TestBotRouteQuerySupportsReadOnlyTextDocumentAndRepeatedTargets(t *testing.T) {
	var calls atomic.Int32
	cfg, h := newRouteRPCFixture(t, func(_ context.Context, request *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error) {
		calls.Add(1)
		if request.PublishResult {
			t.Error("route query published a synthetic result")
		}
		return &xrayapi.RoutingContext{OutboundTag: "block", User: "private-response-secret"}, nil
	})
	payload := []byte(`{"domain":"example.com","port":8443,"user":"private-input-secret","attributes":{"x-test":"value"}}`)
	s, capture := newNativeDocumentBot(t, cfg, payload)
	chat, admin := &tgbotapi.Chat{ID: 9640, Type: "private"}, &tgbotapi.User{ID: 17}
	t.Cleanup(func() { clearRoutingWizard(chat.ID) })
	s.sendRoutingPage(chat.ID, 1, 0, "")
	capture.mu.Lock()
	markup := capture.markup
	capture.mu.Unlock()
	if !strings.Contains(markup, "rt:test") {
		t.Fatal("read-only routing menu omitted route queries")
	}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "rt:test"}
	s.handleCallback(cb)
	if s.handleRoutingInput(&tgbotapi.Message{From: &tgbotapi.User{ID: 18}, Chat: chat, Text: "example.com"}) {
		t.Fatal("another administrator consumed the query wizard")
	}
	s.handleRoutingInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "https://example.com/private"})
	if calls.Load() != 0 {
		t.Fatal("invalid input reached the routing API")
	}
	before, _ := os.ReadFile(h.config)
	for _, message := range []*tgbotapi.Message{
		{From: admin, Chat: chat, Text: "example.com:8443"},
		{From: admin, Chat: chat, Document: &tgbotapi.Document{FileID: "test-file"}},
		{From: admin, Chat: chat, Text: "udp [2001:db8::1]:53"},
	} {
		if !s.handleRoutingInput(message) {
			t.Fatal("valid target did not stay in the current input flow")
		}
		capture.mu.Lock()
		body := capture.text
		capture.mu.Unlock()
		if !strings.Contains(body, "block") || strings.Contains(body, "private-response-secret") || strings.Contains(body, "private-input-secret") {
			t.Fatalf("route result omitted its decision or exposed extra context: %s", body)
		}
	}
	s.handleRoutingInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "/cancel", Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 7}}})
	if s.handleRoutingInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "example.com"}) {
		t.Fatal("cancelled query flow consumed more input")
	}
	after, _ := os.ReadFile(h.config)
	if calls.Load() != 3 || !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("route queries changed configuration, restarted the core or ran an unexpected request")
	}
}

func TestBotRouteQueryNavigationDuringRPCDoesNotOverwriteTheNewMenu(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	cfg, _ := newRouteRPCFixture(t, func(ctx context.Context, _ *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &xrayapi.RoutingContext{OutboundTag: "block"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9641, Type: "private"}, &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "rt:test"}
	s.handleCallback(cb)
	done := make(chan bool, 1)
	go func() { done <- s.handleRoutingInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "example.com"}) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("RPC did not start")
	}
	cb.Data = "rt:page:0"
	s.handleCallback(cb)
	count := capture.messageCount()
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RPC did not finish")
	}
	if capture.messageCount() != count {
		t.Fatal("late routing result overwrote or cluttered the new menu")
	}
}

func TestBotRouteQueryCancellationDuringDocumentDownloadPreventsRPC(t *testing.T) {
	var calls atomic.Int32
	cfg, _ := newRouteRPCFixture(t, func(context.Context, *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error) {
		calls.Add(1)
		return &xrayapi.RoutingContext{OutboundTag: "block"}, nil
	})
	s, transport := newNativeDocumentBot(t, cfg, []byte(`{"domain":"example.com"}`))
	transport.mu.Lock()
	transport.started, transport.release = make(chan struct{}, 1), make(chan struct{})
	started, release := transport.started, transport.release
	transport.mu.Unlock()
	chat, admin := &tgbotapi.Chat{ID: 9642, Type: "private"}, &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "rt:test"}
	s.handleCallback(cb)
	done := make(chan bool, 1)
	go func() {
		done <- s.handleRoutingInput(&tgbotapi.Message{From: admin, Chat: chat, Document: &tgbotapi.Document{FileID: "test-file"}})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	cb.Data = "rt:page:0"
	s.handleCallback(cb)
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not finish")
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled document initiated a routing query")
	}
}

func TestBotRouteQueryServiceEnableRequiresConfirmationAndReturnsToQueryContext(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9643, Type: "private"}, &tgbotapi.User{ID: 17}
	t.Cleanup(func() { clearRoutingWizard(chat.ID) })
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "rt:test"}
	s.handleCallback(cb)
	_, markup := capture.card(t)
	if !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "rt:testenable") {
		t.Fatal("missing explicit API enable action")
	}
	cb.Data = "rt:testenable"
	s.handleCallback(cb)
	before, _ := os.ReadFile(h.config)
	cb.Data = "routing_cancel:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, false)
	if after, _ := os.ReadFile(h.config); !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("cancelling API setup changed configuration")
	}
	cb.Data = "rt:testenable"
	s.handleCallback(cb)
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgbotapi.User{ID: 18}
	s.resolveRoutingChange(cb, true)
	if h.restartCount(t) != 0 {
		t.Fatal("another administrator enabled RoutingService")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	routingWizards.Lock()
	state, ok := routingWizards.items[chat.ID]
	routingWizards.Unlock()
	root, _ := decodeBindingMigrationConfig(h.config)
	if !ok || state.Kind != "route_test" || state.AdminID != admin.ID || !routingServiceConfigured(root) || h.restartCount(t) != 1 {
		t.Fatal("confirmed API setup did not return to the routing query flow")
	}
	data, _ := json.Marshal(root)
	if bytes.Contains(data, []byte(`"listen":"0.0.0.0"`)) {
		t.Fatal("API setup exposed the listener")
	}
}
