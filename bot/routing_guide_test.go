package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type capturedTelegram struct {
	mu     sync.Mutex
	text   string
	markup string
}

func newCapturedRoutingBot(t *testing.T, cfg *BotConfig) (*BotService, *capturedTelegram) {
	t.Helper()
	capture := &capturedTelegram{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse Telegram request: %v", err)
		}
		if r.FormValue("text") != "" {
			capture.mu.Lock()
			capture.text, capture.markup = r.FormValue("text"), r.FormValue("reply_markup")
			capture.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","message_id":1}}`)
	}))
	t.Cleanup(server.Close)
	bot, err := tgbotapi.NewBotAPIWithClient("test", server.URL+"/bot%s/%s", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &BotService{cfg: cfg, bot: bot}, capture
}

func (capture *capturedTelegram) card(t *testing.T) (string, tgbotapi.InlineKeyboardMarkup) {
	t.Helper()
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var markup tgbotapi.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(capture.markup), &markup); err != nil {
		t.Fatalf("decode keyboard %q: %v", capture.markup, err)
	}
	return capture.text, markup
}

func keyboardCallbacks(t *testing.T, markup tgbotapi.InlineKeyboardMarkup) []string {
	t.Helper()
	var callbacks []string
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == nil {
				continue
			}
			if len(*button.CallbackData) > 64 {
				t.Fatalf("callback exceeds Telegram limit: %q", *button.CallbackData)
			}
			callbacks = append(callbacks, *button.CallbackData)
		}
	}
	return callbacks
}

func pendingRoutingToken(t *testing.T, chatID int64) string {
	t.Helper()
	pendingRoutingChanges.Lock()
	defer pendingRoutingChanges.Unlock()
	for token, pending := range pendingRoutingChanges.items {
		if pending.ChatID == chatID {
			return token
		}
	}
	t.Fatal("missing routing confirmation")
	return ""
}

func TestRoutingCardsExposeValuesAndProtectAPI(t *testing.T) {
	h := newBindingMigrationHarness(t, editableRoutingFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	root := editableRoutingRoot(t)

	s.sendRoutingPage(9471, 1, 0, "")
	_, markup := capture.card(t)
	callbacks := keyboardCallbacks(t, markup)
	if !strings.Contains(strings.Join(callbacks, " "), routingRefData("v", routeRef(t, root, 2))) {
		t.Fatal("unnamed rule is not selectable")
	}

	s.sendRoutingRuleDetail(9471, 1, routeRef(t, root, 2))
	body, markup := capture.card(t)
	for _, value := range []string{"domain:example.com", "geosite:cn", "customFlag", "Hidden", "direct", "freedom"} {
		if !strings.Contains(body, value) {
			t.Fatalf("rule detail lacks %q: %s", value, body)
		}
	}
	if strings.Contains(body, "preserve") {
		t.Fatal("unknown extension leaked into Telegram")
	}
	if !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "rt:f:domain:") {
		t.Fatal("rule detail lacks field editing")
	}
	s.sendRoutingMoreFields(9471, 1, routeRef(t, root, 2))
	_, markup = capture.card(t)
	for _, field := range []string{"sourcePort", "localIP", "localPort", "vlessRoute"} {
		if !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "rt:f:"+field+":") {
			t.Fatalf("missing advanced field %s", field)
		}
	}

	s.sendRoutingRuleDetail(9471, 1, routeRef(t, root, 1))
	_, markup = capture.card(t)
	for _, callback := range keyboardCallbacks(t, markup) {
		if strings.HasPrefix(callback, "rt:f:") || strings.HasPrefix(callback, "rt:del:") {
			t.Fatalf("API rule exposes an edit action: %s", callback)
		}
	}
	cfg.AllowMutations = false
	s.sendRoutingRuleDetail(9471, 1, routeRef(t, root, 2))
	_, markup = capture.card(t)
	for _, callback := range keyboardCallbacks(t, markup) {
		if strings.HasPrefix(callback, "rt:f:") {
			t.Fatal("read-only mode exposes editing")
		}
	}
}

func TestGuidedRoutingEditNeedsBoundConfirmation(t *testing.T) {
	h := newBindingMigrationHarness(t, editableRoutingFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, _ := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9472, Type: "private"}, &tgbotapi.User{ID: 17}
	ref := routeRef(t, editableRoutingRoot(t), 2)
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: fmt.Sprintf("rt:f:domain:%d:%s", ref.Index, ref.ETag)}
	s.handleGuidedRoutingCallback(cb)
	msg := &tgbotapi.Message{From: admin, Chat: chat, Text: " "}
	if !s.handleGuidedRoutingInput(msg) {
		t.Fatal("invalid input was not handled")
	}
	if _, ok := getGuidedRoutingWizard(chat.ID, admin.ID, "edit"); !ok {
		t.Fatal("invalid input lost the editing step")
	}
	before, _ := os.ReadFile(h.config)
	msg.Text = "domain:new.example\ngeosite:cn"
	if !s.handleGuidedRoutingInput(msg) {
		t.Fatal("valid input was not handled")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("changed configuration before confirmation")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgbotapi.User{ID: 18}
	s.resolveRoutingChange(cb, true)
	after, _ = os.ReadFile(h.config)
	if !bytes.Equal(before, after) {
		t.Fatal("another admin confirmed this change")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	root, err := decodeBindingMigrationConfig(h.config)
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := routingRules(root)
	rule := rules[1].(map[string]any)
	if rule["customFlag"] != "preserve" || rule["ruleTag"] != nil || rule["domain"].([]any)[0] != "domain:new.example" || h.restartCount(t) != 1 {
		t.Fatalf("wrong confirmed rule: %#v", rule)
	}
	s.resolveRoutingChange(cb, true)
	if h.restartCount(t) != 1 {
		t.Fatal("confirmation was replayed")
	}
}

func TestGuidedRoutingAddAndStaleConfirmation(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%t", stale), func(t *testing.T) {
			h := newBindingMigrationHarness(t, editableRoutingFixture, false)
			cfg := DefaultConfig()
			cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
			s, _ := newCapturedRoutingBot(t, cfg)
			chat, admin := &tgbotapi.Chat{ID: 9473, Type: "private"}, &tgbotapi.User{ID: 17}
			cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "rt:nf:domain"}
			s.handleGuidedRoutingCallback(cb)
			if !s.handleGuidedRoutingInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "domain:new.example"}) {
				t.Fatal("new rule input was not handled")
			}
			cb.Data = "rt:np:" + routingTargetKey(routingTarget{Kind: "outboundTag", Tag: "block"})
			s.handleGuidedRoutingCallback(cb)
			cb.Data = "rt:npos:first"
			s.handleGuidedRoutingCallback(cb)
			cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
			before, _ := os.ReadFile(h.config)
			if stale {
				root, err := decodeBindingMigrationConfig(h.config)
				if err != nil {
					t.Fatal(err)
				}
				rules, _ := routingRules(root)
				rules[2].(map[string]any)["ip"] = []any{"geoip:cn"}
				before, err = json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(h.config, before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.resolveRoutingChange(cb, true)
			after, _ := os.ReadFile(h.config)
			if stale {
				if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
					t.Fatal("stale confirmation changed the new rule ordering")
				}
				return
			}
			root, err := decodeBindingMigrationConfig(h.config)
			if err != nil {
				t.Fatal(err)
			}
			rules, _ := routingRules(root)
			rule := rules[1].(map[string]any)
			if len(rules) != 4 || rule["domain"].([]any)[0] != "domain:new.example" || rule["outboundTag"] != "block" || h.restartCount(t) != 1 {
				t.Fatalf("new rule not inserted after API rule: %#v", rules)
			}
		})
	}
}

func TestDashboardGroupsActions(t *testing.T) {
	for _, lang := range []string{"en", "zh-CN"} {
		callbacks := strings.Join(keyboardCallbacks(t, mainMenuMarkup(lang)), " ")
		if !strings.Contains(callbacks, "menu_routing") || !strings.Contains(callbacks, "menu_tools") || strings.Contains(callbacks, "menu_update ") {
			t.Fatalf("unexpected dashboard actions for %s: %s", lang, callbacks)
		}
	}
}

func TestReturningToMainCancelsRoutingInput(t *testing.T) {
	h := newBindingMigrationHarness(t, editableRoutingFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	s, _ := newCapturedRoutingBot(t, cfg)
	chat := &tgbotapi.Chat{ID: 9474, Type: "private"}
	admin := &tgbotapi.User{ID: 17}
	setGuidedRoutingWizard(chat.ID, guidedRoutingWizard{AdminID: admin.ID, Step: "edit", Expires: time.Now().Add(time.Minute)})
	s.handleCallback(&tgbotapi.CallbackQuery{ID: "test", From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "menu_main"})
	if _, ok := getGuidedRoutingWizard(chat.ID, admin.ID, "edit"); ok {
		t.Fatal("returning home retained a routing input")
	}
}

func TestRoutingMenuExplainsScriptMode(t *testing.T) {
	root := agentFixtureRoot(t)
	root["routing"] = map[string]any{"script": "routing.lua"}
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.AllowMutations = h.config, true
	s, capture := newCapturedRoutingBot(t, cfg)
	s.sendRoutingPage(9475, 1, 0, "")
	body, markup := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(body, "routing script") || strings.Contains(callbacks, "rt:add") || !strings.Contains(callbacks, "ob:page:0") {
		t.Fatalf("script mode not explained: %s %s", body, callbacks)
	}
}
