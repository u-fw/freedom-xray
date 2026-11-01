package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

func TestTelegramClientRedactsTokenFromErrorsAndLogs(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	unreachable := server.URL
	server.Close()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	tg := newTestTelegramClient(t, testSecretBotToken, unreachable, &http.Client{Timeout: 2 * time.Second})
	_, err := tg.sendText(1, "hello", "", nil, false)
	if err == nil || strings.Contains(err.Error(), testSecretBotToken) || !strings.Contains(err.Error(), "BOT_TOKEN") {
		t.Fatalf("transport error was not redacted: %v", err)
	}
	if _, _, _, err := tg.fileURL("file"); err == nil || strings.Contains(err.Error(), testSecretBotToken) {
		t.Fatalf("file lookup error was not redacted: %v", err)
	}
	if strings.Contains(logs.String(), testSecretBotToken) {
		t.Fatalf("token reached the log: %s", logs.String())
	}
}

func TestTelegramCommandParsing(t *testing.T) {
	cases := []struct {
		text    string
		length  int
		command string
		args    string
	}{
		{"/adduser phone cdn", 8, "adduser", "phone cdn"},
		{"/start@freedom_bot", 18, "start", ""},
		{"/cancel", 7, "cancel", ""},
		{"/language  zh-CN", 9, "language", "zh-CN"},
	}
	for _, test := range cases {
		message := &tgMessage{Text: test.text, Entities: []tgEntity{{Type: "bot_command", Offset: 0, Length: test.length}}}
		if !message.IsCommand() || message.Command() != test.command || message.CommandArguments() != test.args {
			t.Fatalf("%q parsed as %q %q", test.text, message.Command(), message.CommandArguments())
		}
	}
	for _, message := range []*tgMessage{
		nil,
		{Text: "/start"},
		{Text: "hello /start", Entities: []tgEntity{{Type: "bot_command", Offset: 6, Length: 6}}},
		{Text: "/x", Entities: []tgEntity{{Type: "bot_command", Offset: 0, Length: 50}}},
		{Text: "*bold*", Entities: []tgEntity{{Type: "bold", Offset: 0, Length: 6}}},
	} {
		if message.IsCommand() || message.Command() != "" || message.CommandArguments() != "" {
			t.Fatalf("%+v must not parse as a command", message)
		}
	}
}

func TestTelegramMarkupConversion(t *testing.T) {
	if telegramMarkup(nil) != nil || telegramMarkup(&tgKeyboard{}) != nil || telegramMarkup(&tgKeyboard{InlineKeyboard: [][]tgButton{{}}}) != nil {
		t.Fatal("empty keyboards must be omitted")
	}
	markup := telegramMarkup(&tgKeyboard{InlineKeyboard: [][]tgButton{{newButton("Open", "menu_main"), {Text: "Copy", Copy: "vless://example"}}, {}}})
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("unexpected markup: %+v", markup)
	}
	open, copyButton := markup.InlineKeyboard[0][0], markup.InlineKeyboard[0][1]
	if open.CallbackData != "menu_main" || open.CopyText != nil || copyButton.CopyText == nil || copyButton.CopyText.Text != "vless://example" || copyButton.CallbackData != "" {
		t.Fatalf("unexpected buttons: %+v %+v", open, copyButton)
	}
}

func TestTelegramClientOmitsEmptyReplyMarkup(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := telegramRequestValues(t, r)
		values["method"] = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		requests = append(requests, values)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":7,"date":0,"chat":{"id":1,"type":"private"}}}`)
	}))
	t.Cleanup(server.Close)
	tg := newTestTelegramClient(t, testBotToken, server.URL, server.Client())
	keyboard := newKeyboard(newRow(newButton("Home", "menu_main")))
	if id, err := tg.sendText(1, "plain", "", nil, true); err != nil || id != 7 {
		t.Fatalf("send: %d %v", id, err)
	}
	if _, err := tg.sendText(1, "card", "Markdown", &keyboard, false); err != nil {
		t.Fatal(err)
	}
	if err := tg.editText(1, 7, "edited", "", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("expected three requests, got %d", len(requests))
	}
	plain, card, edit := requests[0], requests[1], requests[2]
	if _, ok := plain["reply_markup"]; ok || plain["parse_mode"] != "" || !strings.Contains(plain["link_preview_options"], `"is_disabled":true`) {
		t.Fatalf("plain message sent unexpected options: %v", plain)
	}
	if card["parse_mode"] != "Markdown" || !strings.Contains(card["reply_markup"], `"callback_data":"menu_main"`) {
		t.Fatalf("card lost its keyboard or parse mode: %v", card)
	}
	if _, ok := edit["reply_markup"]; ok || edit["method"] != "editMessageText" || edit["message_id"] != "7" {
		t.Fatalf("edit without keyboard sent unexpected fields: %v", edit)
	}
}

func TestTelegramUpdateConversion(t *testing.T) {
	update, ok := convertTelegramUpdate(telego.Update{CallbackQuery: &telego.CallbackQuery{
		ID: "q", From: telego.User{ID: 17, LanguageCode: "zh-hans"}, Data: "menu_main",
		Message: &telego.InaccessibleMessage{Chat: telego.Chat{ID: 17, Type: "private"}, MessageID: 9},
	}})
	if !ok || update.CallbackQuery == nil || update.CallbackQuery.From.ID != 17 || update.CallbackQuery.Message == nil ||
		update.CallbackQuery.Message.MessageID != 9 || !update.CallbackQuery.Message.Chat.IsPrivate() || update.CallbackQuery.Data != "menu_main" {
		t.Fatalf("unexpected callback conversion: %+v", update.CallbackQuery)
	}
	update, ok = convertTelegramUpdate(telego.Update{Message: &telego.Message{
		MessageID: 3, Chat: telego.Chat{ID: 17, Type: "private"}, From: &telego.User{ID: 17},
		Text: "/start", Entities: []telego.MessageEntity{{Type: "bot_command", Offset: 0, Length: 6}},
		Document: &telego.Document{FileID: "f", FileName: "rule.json", FileSize: 12},
	}})
	if !ok || update.Message.Command() != "start" || update.Message.Document == nil || update.Message.Document.FileName != "rule.json" {
		t.Fatalf("unexpected message conversion: %+v", update.Message)
	}
	if _, ok := convertTelegramUpdate(telego.Update{}); ok {
		t.Fatal("updates without a message or callback must be ignored")
	}
}
