package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type nativeDocumentTransport struct {
	mu        sync.Mutex
	payload   []byte
	filePath  string
	redirect  bool
	downloads int
	foreign   int
	markup    string
	documents [][]byte
	started   chan struct{}
	release   chan struct{}
}

func (m *nativeDocumentTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host == "api.telegram.org" {
		m.mu.Lock()
		m.downloads++
		redirect, payload, started, release := m.redirect, m.payload, m.started, m.release
		m.mu.Unlock()
		if started != nil {
			started <- struct{}{}
			<-release
		}
		if redirect {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://attacker.invalid/"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: -1, Body: io.NopCloser(bytes.NewReader(payload)), Request: request}, nil
	}
	if request.URL.Host == "attacker.invalid" {
		m.mu.Lock()
		m.foreign++
		m.mu.Unlock()
		return nil, fmt.Errorf("unexpected redirect")
	}
	return http.DefaultTransport.RoundTrip(request)
}

func newNativeDocumentBot(t *testing.T, cfg *BotConfig, payload []byte) (*BotService, *nativeDocumentTransport) {
	t.Helper()
	m := &nativeDocumentTransport{payload: payload, filePath: "documents/input.json"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			m.mu.Lock()
			filePath := m.filePath
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"file_id": "test-file", "file_path": filePath}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendDocument") {
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Errorf("parse document: %v", err)
			} else if document, _, err := r.FormFile("document"); err != nil {
				t.Errorf("read document: %v", err)
			} else {
				data, err := io.ReadAll(document)
				_ = document.Close()
				if err != nil {
					t.Errorf("read document contents: %v", err)
				}
				m.mu.Lock()
				m.documents = append(m.documents, data)
				m.mu.Unlock()
			}
		} else {
			_ = r.ParseForm()
			if markup := r.FormValue("reply_markup"); markup != "" {
				m.mu.Lock()
				m.markup = markup
				m.mu.Unlock()
			}
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","message_id":1}}`)
	}))
	t.Cleanup(server.Close)
	client := &http.Client{Transport: m}
	bot, err := tgbotapi.NewBotAPIWithClient("secret-test-token", server.URL+"/bot%s/%s", client)
	if err != nil {
		t.Fatal(err)
	}
	return &BotService{cfg: cfg, bot: bot}, m
}

func nativeExportCallback(t *testing.T, capture *nativeDocumentTransport) string {
	t.Helper()
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var markup tgbotapi.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(capture.markup), &markup); err != nil {
		t.Fatal(err)
	}
	for _, callback := range keyboardCallbacks(t, markup) {
		if strings.HasPrefix(callback, "ni:exconfirm:") {
			return callback
		}
	}
	t.Fatal("missing sensitive export confirmation")
	return ""
}

func TestNativeInboundExportChecksPermissionOwnerAndFreshness(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.AllowSensitiveBackup = h.config, true
	s, capture := newNativeDocumentBot(t, cfg, nil)
	chat, admin := &tgbotapi.Chat{ID: 9563, Type: "private"}, &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}}
	ask := func() string {
		root, err := decodeBindingMigrationConfig(h.config)
		if err != nil {
			t.Fatal(err)
		}
		records, err := nativeInboundRecords(root)
		if err != nil {
			t.Fatal(err)
		}
		cb.From, cb.Data = admin, nativeInboundCallback("export", 1, records[0].ETag)
		s.handleNativeInboundCallback(cb)
		return nativeExportCallback(t, capture)
	}
	count := func() int {
		capture.mu.Lock()
		defer capture.mu.Unlock()
		return len(capture.documents)
	}
	cfg.AllowSensitiveBackup = false
	root, _ := decodeBindingMigrationConfig(h.config)
	records, _ := nativeInboundRecords(root)
	cb.Data = nativeInboundCallback("export", 1, records[0].ETag)
	s.handleNativeInboundCallback(cb)
	pendingNativeInboundExports.Lock()
	for _, pending := range pendingNativeInboundExports.items {
		if pending.ChatID == chat.ID {
			pendingNativeInboundExports.Unlock()
			t.Fatal("disabled sensitive exports created a confirmation")
		}
	}
	pendingNativeInboundExports.Unlock()
	cfg.AllowSensitiveBackup = true
	first := ask()
	cb.From, cb.Data = &tgbotapi.User{ID: 18}, first
	s.handleNativeInboundCallback(cb)
	if count() != 0 {
		t.Fatal("another admin exported the inbound")
	}
	root, _ = decodeBindingMigrationConfig(h.config)
	root["inbounds"].([]any)[0].(map[string]any)["port"] = json.Number("28444")
	changed, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, changed, 0600); err != nil {
		t.Fatal(err)
	}
	cb.From = admin
	s.handleNativeInboundCallback(cb)
	if count() != 0 {
		t.Fatal("stale confirmation exported a modified inbound")
	}
	second := ask()
	cfg.AllowSensitiveBackup = false
	cb.Data = second
	s.handleNativeInboundCallback(cb)
	if count() != 0 {
		t.Fatal("revoked sensitive-export permission was ignored")
	}
	cfg.AllowSensitiveBackup = true
	third := ask()
	cb.Data = strings.Replace(third, "ni:exconfirm:", "ni:excancel:", 1)
	s.handleNativeInboundCallback(cb)
	cb.Data = third
	s.handleNativeInboundCallback(cb)
	if count() != 0 {
		t.Fatal("cancelled confirmation remained usable")
	}
	fourth := ask()
	cb.Data = fourth
	s.handleNativeInboundCallback(cb)
	if count() != 1 {
		t.Fatal("authorized export was not sent")
	}
	capture.mu.Lock()
	var exported map[string]any
	err := json.Unmarshal(capture.documents[0], &exported)
	capture.mu.Unlock()
	if err != nil || exported["tag"] != "raw" || exported["inbounds"] != nil {
		t.Fatal("export did not contain exactly the selected inbound")
	}
	s.handleNativeInboundCallback(cb)
	if count() != 1 {
		t.Fatal("sensitive export confirmation could be replayed")
	}
}

func TestNativeInboundDocumentDownloadIsBoundedAndDoesNotFollowRedirects(t *testing.T) {
	cfg := DefaultConfig()
	s, transport := newNativeDocumentBot(t, cfg, []byte(`{"protocol":"socks"}`))
	document := &tgbotapi.Document{FileID: "test-file"}
	data, err := readTelegramJSONDocument(s.bot, document)
	if err != nil || !bytes.Contains(data, []byte("socks")) {
		t.Fatalf("document download failed: %v", err)
	}
	for _, unsafePath := range []string{"../escape", "/absolute", "documents/../escape", "documents/%2e%2e/escape", "https://attacker.invalid"} {
		transport.mu.Lock()
		transport.filePath = unsafePath
		before := transport.downloads
		transport.mu.Unlock()
		if _, err := readTelegramJSONDocument(s.bot, document); err == nil || strings.Contains(err.Error(), "secret-test-token") {
			t.Fatalf("accepted unsafe path or exposed a token: %q %v", unsafePath, err)
		}
		transport.mu.Lock()
		after := transport.downloads
		transport.mu.Unlock()
		if after != before {
			t.Fatal("downloaded an unsafe path")
		}
	}
	transport.mu.Lock()
	transport.filePath, transport.redirect = "documents/input.json", true
	transport.mu.Unlock()
	if _, err := readTelegramJSONDocument(s.bot, document); err == nil || strings.Contains(err.Error(), "secret-test-token") {
		t.Fatalf("accepted redirect or exposed token: %v", err)
	}
	transport.mu.Lock()
	if transport.foreign != 0 {
		t.Fatal("followed a redirect containing the Bot token")
	}
	transport.redirect = false
	transport.payload = bytes.Repeat([]byte("x"), telegramJSONDocumentLimit+1)
	transport.mu.Unlock()
	if _, err := readTelegramJSONDocument(s.bot, document); err == nil {
		t.Fatal("accepted an oversized streaming response")
	}
	if _, err := readTelegramJSONDocument(s.bot, &tgbotapi.Document{FileID: "test-file", FileSize: telegramJSONDocumentLimit + 1}); err == nil {
		t.Fatal("accepted an oversized Telegram document")
	}
}

func TestNativeInboundWizardAcceptsJSONDocument(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	payload, _ := json.Marshal(map[string]any{"tag": "uploaded", "protocol": "socks", "port": 32001, "settings": map[string]any{"auth": "noauth", "extension": strings.Repeat("x", 5000)}})
	s, _ := newNativeDocumentBot(t, cfg, payload)
	chat, admin := &tgbotapi.Chat{ID: 9564, Type: "private"}, &tgbotapi.User{ID: 17}
	root, _ := decodeBindingMigrationConfig(h.config)
	etag, _ := nativeInboundListETag(root)
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: "ni:add:" + etag}
	s.handleNativeInboundCallback(cb)
	before, _ := os.ReadFile(h.config)
	if !s.handleNativeInboundInput(&tgbotapi.Message{From: admin, Chat: chat, Document: &tgbotapi.Document{FileID: "test-file"}}) {
		t.Fatal("document was not handled")
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("document changed configuration before confirmation")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	if len(root["inbounds"].([]any)) != 2 || h.restartCount(t) != 1 {
		t.Fatal("confirmed JSON document was not applied")
	}
}
