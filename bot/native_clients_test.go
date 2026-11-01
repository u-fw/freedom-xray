package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func nativeClientsFixture(t *testing.T, protocol string) (map[string]any, map[string]any) {
	t.Helper()
	root := agentFixtureRoot(t)
	client := map[string]any{"email": "native-user", "level": json.Number("2"), "extension": "retained"}
	if protocol == "vmess" {
		client["id"] = "22222222-2222-4222-8222-222222222222"
	} else {
		client["password"] = "original-test-secret"
	}
	inbound := map[string]any{"tag": "native", "protocol": protocol, "listen": "127.0.0.1", "port": json.Number("32001"), "settings": map[string]any{"clients": nil, "users": []any{client}}}
	root["inbounds"] = append(root["inbounds"].([]any), inbound)
	root["routing"].(map[string]any)["rules"] = []any{map[string]any{"type": "field", "user": []any{"native-user"}, "outboundTag": "direct"}}
	return root, inbound
}

func nativeClientRef(t *testing.T, inbound map[string]any, kind string, index int, value string) nativeClientChange {
	t.Helper()
	etag, err := routingRuleETag(inbound)
	if err != nil {
		t.Fatal(err)
	}
	return nativeClientChange{Kind: kind, InboundIndex: 2, InboundETag: etag, ClientIndex: index, Value: value}
}

func TestNativeClientsCRUDPreservesAliasesAndExtensions(t *testing.T) {
	for _, protocol := range []string{"vmess", "trojan"} {
		t.Run(protocol, func(t *testing.T) {
			root, inbound := nativeClientsFixture(t, protocol)
			cfg := DefaultConfig()
			cfg.XrayConfigPath = ""
			if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "add", 0, "alice"), cfg); err == nil {
				t.Fatal("accepted a managed VLESS identity")
			}
			if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "add", 0, bindingEmail("alice", "raw")), cfg); err == nil {
				t.Fatal("accepted a managed per-inbound identity")
			}
			if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "add", 0, "native-user"), cfg); err == nil {
				t.Fatal("accepted a duplicate identity")
			}
			if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "add", 0, "another+device@example.com"), cfg); err != nil {
				t.Fatal(err)
			}
			settings := inbound["settings"].(map[string]any)
			if settings["clients"] != nil || len(settings["users"].([]any)) != 2 {
				t.Fatal("changed inactive alias")
			}
			records, err := nativeClientRecords(inbound)
			data, _ := json.Marshal(records)
			if err != nil || len(records) != 2 || bytes.Contains(data, []byte("original-test-secret")) || bytes.Contains(data, []byte("22222222")) {
				t.Fatalf("client summaries leaked credentials: %s %v", data, err)
			}
			client, _ := lookupNativeClient(inbound, 1)
			field := "id"
			if protocol == "trojan" {
				field = "password"
			}
			oldCredential := client[field]
			if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "rotate", 1, ""), cfg); err != nil || client[field] == oldCredential || client["extension"] != "retained" {
				t.Fatalf("rotation failed or lost an extension: %v", err)
			}
			if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "level", 1, "7"), cfg); err != nil {
				t.Fatal(err)
			}
			levels := root["policy"].(map[string]any)["levels"].(map[string]any)
			if levels["7"] == nil {
				t.Fatal("new policy level lacks traffic statistics")
			}
			change := nativeClientRef(t, inbound, "email", 1, "renamed")
			change.RoutingETag, _ = routingListETag(root)
			preview, err := applyNativeClientChange(root, change, cfg)
			if err != nil || !reflect.DeepEqual(preview["routing_rules"], []int{1}) {
				t.Fatalf("routing impact not reported: %v %v", preview, err)
			}
			if rules, _ := routingRules(root); rules[0].(map[string]any)["user"].([]any)[0] != "native-user" {
				t.Fatal("shared routing rules were silently rewritten")
			}
			change = nativeClientRef(t, inbound, "remove", 2, "")
			change.RoutingETag, _ = routingListETag(root)
			if _, err := applyNativeClientChange(root, change, cfg); err != nil {
				t.Fatal(err)
			}
			if len(settings["users"].([]any)) != 1 || client["email"] != "renamed" {
				t.Fatal("client changes were not applied")
			}
		})
	}
}

func TestNativeClientAgentRequiresFreshReviewedRouting(t *testing.T) {
	root, _ := nativeClientsFixture(t, "trojan")
	fixture, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(fixture), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath = h.config, h.xray
	records, _ := nativeInboundRecords(root)
	op, _ := findAgentOperation("inbounds.clients.remove")
	request := map[string]any{"index": 2, "etag": records[1].ETag, "client_index": 1}
	raw, _ := json.Marshal(request)
	before, _ := os.ReadFile(h.config)
	preview, err := executeAgentOperation(cfg, op, raw, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(preview)
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || bytes.Contains(encoded, []byte("original-test-secret")) || h.restartCount(t) != 0 {
		t.Fatal("preview wrote configuration or leaked a credential")
	}
	if _, err := executeAgentOperation(cfg, op, raw, true, false); err == nil {
		t.Fatal("removal did not require reviewed routing")
	}
	impact := preview.(map[string]any)["preview"].(map[string]any)
	request["routing_etag"] = impact["routing_etag"]
	root["routing"].(map[string]any)["rules"] = []any{map[string]any{"type": "field", "user": []any{"other"}, "outboundTag": "direct"}}
	changed, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, changed, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(request)
	if _, err := executeAgentOperation(cfg, op, raw, true, false); !errors.Is(err, errRoutingRuleChanged) {
		t.Fatalf("accepted stale routing impact: %v", err)
	}
	if h.restartCount(t) != 0 {
		t.Fatal("stale request restarted Xray")
	}
	delete(request, "routing_etag")
	raw, _ = json.Marshal(request)
	preview, err = executeAgentOperation(cfg, op, raw, false, false)
	if err != nil {
		t.Fatal(err)
	}
	request["routing_etag"] = preview.(map[string]any)["preview"].(map[string]any)["routing_etag"]
	raw, _ = json.Marshal(request)
	if _, err := executeAgentOperation(cfg, op, raw, true, false); err != nil || h.restartCount(t) != 1 {
		t.Fatalf("reviewed removal failed: %v", err)
	}
	var output bytes.Buffer
	if code := runAgentCLI([]string{"inbounds.clients.export"}, strings.NewReader(`{"index":2,"etag":"123456789012345678901234","client_index":1}`), &output); code != 3 {
		t.Fatalf("credential export not gated: %s", output.String())
	}
}

func TestNativeClientRemovalRejectsRoutingScript(t *testing.T) {
	root, inbound := nativeClientsFixture(t, "trojan")
	root["routing"].(map[string]any)["script"] = "custom-script"
	cfg := DefaultConfig()
	cfg.XrayConfigPath = ""
	if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "remove", 1, ""), cfg); err == nil || !strings.Contains(err.Error(), "routing script") {
		t.Fatalf("accepted removal with uninspectable routing: %v", err)
	}
}

func TestNativeClientAddKeepsShadowedUsersInactive(t *testing.T) {
	root, inbound := nativeClientsFixture(t, "vmess")
	settings := inbound["settings"].(map[string]any)
	shadow := settings["users"]
	settings["clients"] = []any{}
	cfg := DefaultConfig()
	cfg.XrayConfigPath = ""
	if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "add", 0, "new"), cfg); err != nil {
		t.Fatal(err)
	}
	if len(settings["clients"].([]any)) != 1 || !reflect.DeepEqual(settings["users"], shadow) {
		t.Fatal("creation changed the inactive users array")
	}
}

func TestNativeClientBotExportIsExplicitAndScoped(t *testing.T) {
	root, _ := nativeClientsFixture(t, "trojan")
	fixture, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(fixture), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	var mu sync.Mutex
	var file []byte
	var caption string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendDocument") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse document: %v", err)
			} else if document, _, err := r.FormFile("document"); err != nil {
				t.Errorf("read document: %v", err)
			} else {
				defer document.Close()
				data, err := io.ReadAll(document)
				if err != nil {
					t.Errorf("read document content: %v", err)
				}
				mu.Lock()
				file, caption = data, r.FormValue("caption")
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","message_id":1}}`))
	}))
	t.Cleanup(server.Close)
	bot, err := tgbotapi.NewBotAPIWithClient("test", server.URL+"/bot%s/%s", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	s := &BotService{cfg: cfg, bot: bot}
	records, _ := nativeInboundRecords(root)
	cb := &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 17}, Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: 9562, Type: "private"}}, Data: nativeClientCallback("export", 2, 1, records[1].ETag)}
	s.handleNativeClientCallback(cb)
	mu.Lock()
	defer mu.Unlock()
	var client map[string]any
	if err := json.Unmarshal(file, &client); err != nil || client["password"] != "original-test-secret" || client["email"] != "native-user" || client["inbounds"] != nil || !strings.Contains(caption, "not end-to-end encrypted") {
		t.Fatalf("incorrect scoped client export: %v %q", err, caption)
	}
}

func TestNativeClientBotFlowIsBoundAndRedacted(t *testing.T) {
	root, _ := nativeClientsFixture(t, "trojan")
	fixture, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(fixture), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9561, Type: "private"}, &tgbotapi.User{ID: 17}
	records, _ := nativeInboundRecords(root)
	etag := records[1].ETag
	s.sendNativeInboundDetail(chat.ID, 1, 2, etag)
	_, markup := capture.card(t)
	if !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "nc:list:") {
		t.Fatal("inbound lacks a client menu")
	}
	s.sendNativeClientDetail(chat.ID, 1, 2, 1, etag)
	body, markup := capture.card(t)
	if strings.Contains(body, "original-test-secret") || !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "nc:rotate:") {
		t.Fatal("client detail leaked credentials or lacks controls")
	}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: nativeClientCallback("email", 2, 1, etag)}
	s.handleNativeClientCallback(cb)
	if !s.handleNativeInboundInput(&tgbotapi.Message{From: admin, Chat: chat, Text: "renamed"}) {
		t.Fatal("client email input was not handled")
	}
	body, _ = capture.card(t)
	if !strings.Contains(body, "1") || strings.Contains(body, "original-test-secret") {
		t.Fatal("confirmation omitted routing impact or leaked credentials")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgbotapi.User{ID: 18}
	s.resolveRoutingChange(cb, true)
	if h.restartCount(t) != 0 {
		t.Fatal("another administrator confirmed the change")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	root, _ = decodeBindingMigrationConfig(h.config)
	inbound := root["inbounds"].([]any)[1].(map[string]any)
	client, _ := lookupNativeClient(inbound, 1)
	if client["email"] != "renamed" || h.restartCount(t) != 1 {
		t.Fatal("confirmed client change was not applied")
	}
	s.resolveRoutingChange(cb, true)
	if h.restartCount(t) != 1 {
		t.Fatal("confirmation could be replayed")
	}
}

func TestNativeClientsPassRealXrayValidation(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	for _, protocol := range []string{"vmess", "trojan"} {
		root, inbound := nativeClientsFixture(t, protocol)
		cfg := DefaultConfig()
		cfg.XrayConfigPath = ""
		if _, err := applyNativeClientChange(root, nativeClientRef(t, inbound, "add", 0, "generated"), cfg); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(root)
		if output, err := exec.Command(bin, "run", "-test", "-config", writeRoutingConfig(t, string(data))).CombinedOutput(); err != nil {
			t.Fatalf("Xray rejected %s clients: %v\n%s", protocol, err, output)
		}
	}
}
