package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNativeBalancingRejectsCaseAliasProtectionBypasses(t *testing.T) {
	for _, input := range []string{
		`{"tag":"pool","selector":["direct"],"FallbackTag":"api"}`,
		`{"tag":"pool","selector":["direct"],"Strategy":{"Type":"leastPing"}}`,
		`{"tag":"pool","selector":["direct"],"Selector":["api"]}`,
		`{"tag":"pool","selector":["direct"],"strategy":{"type":"random","Type":"leastPing"}}`,
	} {
		t.Run(input, func(t *testing.T) {
			root := agentFixtureRoot(t)
			before, _ := json.Marshal(root)
			item, _ := decodeNetworkObject([]byte(input))
			if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: item}); err == nil {
				t.Fatal("accepted an ambiguous or protected native balancer")
			}
			after, _ := json.Marshal(root)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected native input partially mutated the configuration")
			}
		})
	}
	for _, input := range []string{
		`{"SubjectSelector":["api"]}`,
		`{"subjectSelector":["direct"],"SubjectSelector":["api"]}`,
		`{"subjectSelector":["direct"],"pingConfig":{"interval":"1m","Interval":"1s"}}`,
	} {
		root := agentFixtureRoot(t)
		item, _ := decodeNetworkObject([]byte(input))
		if _, err := applyObservatoryChange(root, observatoryChange{Kind: "burstObservatory", Action: "put", Config: item}); err == nil {
			t.Fatalf("accepted ambiguous/protected monitor input: %s", input)
		}
		if root["burstObservatory"] != nil {
			t.Fatal("rejected monitor input changed the configuration")
		}
	}
}

func TestNativeBalancingCanonicalizationPreservesExtensionsAndPatchSettings(t *testing.T) {
	root := agentFixtureRoot(t)
	item, _ := decodeNetworkObject([]byte(`{"Tag":"pool","Selector":["direct"],"Strategy":{"Type":"random","Settings":{"KeepCase":"native-extension"}}}`))
	if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: item}); err != nil {
		t.Fatal(err)
	}
	_, saved, _ := findBalancer(root, "pool")
	if saved["Tag"] != nil || saved["selector"] == nil || item["Tag"] != "pool" {
		t.Fatal("fields were not normalized on a copy")
	}
	strategy, _ := getMap(saved["strategy"])
	settings, _ := getMap(strategy["settings"])
	if settings["KeepCase"] != "native-extension" {
		t.Fatal("arbitrary native map keys were changed")
	}
	monitor, _ := decodeNetworkObject([]byte(`{"SubjectSelector":["direct"],"PingConfig":{"Interval":"1m","Sampling":4,"Timeout":"5s","Destination":"https://example.test/old"}}`))
	root["burstObservatory"] = monitor
	etag, _ := routingRuleETag(monitor)
	if _, err := applyObservatoryChange(root, observatoryChange{Kind: "burstObservatory", Action: "field", Field: "url", Value: "https://example.test/new", ETag: etag}); err != nil {
		t.Fatal(err)
	}
	updated, _ := readObservatory(root, "burstObservatory")
	ping, _ := getMap(updated["pingConfig"])
	original, _ := getMap(monitor["PingConfig"])
	if ping["sampling"] != json.Number("4") || ping["timeout"] != "5s" || ping["destination"] != "https://example.test/new" || original["Destination"] != "https://example.test/old" {
		t.Fatal("guided editing lost advanced fields or mutated its source")
	}
	// Legacy aliases must not allow removal of a monitor still needed by a balancer.
	root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "legacy", "selector": []any{"direct"}, "Strategy": map[string]any{"Type": "leastPing"}}}
	etag, _ = routingRuleETag(updated)
	if _, err := applyObservatoryChange(root, observatoryChange{Kind: "burstObservatory", Action: "delete", ETag: etag}); err == nil {
		t.Fatal("removed a monitor needed by a legacy case-alias strategy")
	}
}

func TestBotNativeBalancingCreateAndReplaceRequiresConfirmation(t *testing.T) {
	for i, kind := range []string{"balancer", "observatory", "burstObservatory"} {
		t.Run(kind, func(t *testing.T) {
			h := newBindingMigrationHarness(t, agentFixture, false)
			cfg := DefaultConfig()
			cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
			s, capture := newNativeDocumentBot(t, cfg, nil)
			chat, admin := &tgChat{ID: 9600 + int64(i), Type: "private"}, &tgUser{ID: 17}
			t.Cleanup(func() { clearBalancerWizard(chat.ID) })
			cb := &tgCallback{From: admin, Message: &tgMessage{MessageID: 1, Chat: chat}}
			for phase := 0; phase < 2; phase++ {
				root, _ := decodeBindingMigrationConfig(h.config)
				payload := map[string]any{"subjectSelector": []any{"direct"}, "probeInterval": "1m", "probeURL": "https://example.test/?token=native-secret", "enableConcurrency": true}
				ref := "new"
				if kind == "balancer" {
					payload = map[string]any{"tag": "pool", "selector": []any{"direct"}, "strategy": map[string]any{"type": "random", "settings": map[string]any{"extension": "native-secret"}}}
					cb.Data = "bl:jsonnew"
					if phase == 1 {
						records, _ := balancerRecords(root)
						cb.Data = balancerCallback("json", "pool", records[0].ETag)
					}
				} else {
					if kind == "burstObservatory" {
						payload = map[string]any{"subjectSelector": []any{"direct"}, "pingConfig": map[string]any{"interval": "1m", "sampling": 4, "timeout": "5s", "destination": "https://example.test/?token=native-secret"}}
					}
					if phase == 1 {
						config, _ := readObservatory(root, kind)
						ref, _ = routingRuleETag(config)
					}
					cb.Data = observatoryCallback("json", kind, ref)
				}
				if phase == 1 {
					payload["customExtension"] = "updated-native-secret"
				}
				s.handleCallback(cb)
				if _, ok := getBalancerWizard(chat.ID, admin.ID); !ok {
					t.Fatal("native JSON button did not start a wizard")
				}
				if s.handleBalancerInput(&tgMessage{From: &tgUser{ID: 18}, Chat: chat, Text: `{}`}) {
					t.Fatal("another admin consumed the wizard")
				}
				s.handleBalancerInput(&tgMessage{From: admin, Chat: chat, Text: `[`})
				if _, ok := getBalancerWizard(chat.ID, admin.ID); !ok {
					t.Fatal("invalid input discarded the current step")
				}
				before, _ := os.ReadFile(h.config)
				data, _ := json.Marshal(payload)
				msg := &tgMessage{From: admin, Chat: chat, Text: string(data)}
				if (phase+i)%2 == 1 {
					capture.mu.Lock()
					capture.payload = data
					capture.mu.Unlock()
					msg.Text, msg.Document = "", &tgDocument{FileID: "test-file"}
				}
				if !s.handleBalancerInput(msg) {
					t.Fatal("native JSON was not handled")
				}
				after, _ := os.ReadFile(h.config)
				capture.mu.Lock()
				text := capture.text
				capture.mu.Unlock()
				if !bytes.Equal(before, after) || h.restartCount(t) != phase || strings.Contains(text, "native-secret") {
					t.Fatal("unconfirmed native input wrote configuration or exposed sensitive values")
				}
				cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
				cb.From = &tgUser{ID: 18}
				s.resolveRoutingChange(cb, true)
				if h.restartCount(t) != phase {
					t.Fatal("another admin applied the pending native input")
				}
				cb.From = admin
				s.resolveRoutingChange(cb, true)
				if h.restartCount(t) != phase+1 {
					t.Fatal("confirmation was not applied exactly once")
				}
				root, _ = decodeBindingMigrationConfig(h.config)
				var saved map[string]any
				if kind == "balancer" {
					_, saved, _ = findBalancer(root, "pool")
				} else {
					saved, _ = readObservatory(root, kind)
				}
				savedJSON, _ := json.Marshal(saved)
				if !bytes.Equal(savedJSON, data) {
					t.Fatal("native replacement lost advanced fields")
				}
				fresh, _ := routingRuleETag(saved)
				button := observatoryCallback("json", kind, fresh)
				if kind == "balancer" {
					button = balancerCallback("json", "pool", fresh)
				}
				capture.mu.Lock()
				markup := capture.markup
				capture.mu.Unlock()
				if !strings.Contains(markup, button) {
					t.Fatal("successful edit did not return to detail with fresh buttons")
				}
				s.resolveRoutingChange(cb, true)
				if h.restartCount(t) != phase+1 {
					t.Fatal("confirmation was replayed")
				}
			}
		})
	}
}

func TestNativeBalancingConfirmationsRejectStaleConfigurationAndRevokedPermission(t *testing.T) {
	for i, kind := range []string{"balancer", "observatory", "burstObservatory"} {
		t.Run(kind, func(t *testing.T) {
			root := agentFixtureRoot(t)
			var saved map[string]any
			if kind == "balancer" {
				saved = testBalancer("pool")
				root["routing"].(map[string]any)["balancers"] = []any{saved}
			} else {
				saved = map[string]any{"subjectSelector": []any{"direct"}, "pingConfig": map[string]any{}}
				root[kind] = saved
			}
			fixture, _ := json.Marshal(root)
			h := newBindingMigrationHarness(t, string(fixture), false)
			cfg := DefaultConfig()
			cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
			s, _ := newNativeDocumentBot(t, cfg, nil)
			chat, admin := &tgChat{ID: 9620 + int64(i), Type: "private"}, &tgUser{ID: 17}
			t.Cleanup(func() { clearBalancerWizard(chat.ID) })
			cb := &tgCallback{From: admin, Message: &tgMessage{MessageID: 1, Chat: chat}}
			for _, revoke := range []bool{false, true} {
				etag, _ := routingRuleETag(saved)
				cb.Data = observatoryCallback("json", kind, etag)
				if kind == "balancer" {
					cb.Data = balancerCallback("json", "pool", etag)
				}
				s.handleCallback(cb)
				if kind == "balancer" {
					s.handleBalancerInput(&tgMessage{Chat: chat, From: admin, Text: `{"tag":"renamed","selector":["direct"]}`})
					if _, ok := getBalancerWizard(chat.ID, admin.ID); !ok {
						t.Fatal("invalid tag rename discarded the wizard")
					}
				}
				payload, _ := copyNativeObjectFields(saved, nil)
				payload["extension"] = "new-value"
				input, _ := json.Marshal(payload)
				s.handleBalancerInput(&tgMessage{Chat: chat, From: admin, Text: string(input)})
				cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
				if revoke {
					cfg.AllowMutations = false
				} else {
					saved["externalChange"] = true
					changed, _ := json.Marshal(root)
					if err := os.WriteFile(h.config, changed, 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := os.ReadFile(h.config)
				s.resolveRoutingChange(cb, true)
				after, _ := os.ReadFile(h.config)
				if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
					t.Fatal("stale or permission-revoked confirmation changed the configuration")
				}
			}
		})
	}
}

func TestNativeBalancingDownloadCannotRestoreCancelledWizard(t *testing.T) {
	for i, callback := range []string{"bl:jsonnew", "mo:json:o:new", "mo:json:b:new"} {
		t.Run(callback, func(t *testing.T) {
			h := newBindingMigrationHarness(t, agentFixture, false)
			cfg := DefaultConfig()
			cfg.XrayConfigPath, cfg.AllowMutations = h.config, true
			s, transport := newNativeDocumentBot(t, cfg, []byte(`{"tag":"pool","selector":["direct"],"subjectSelector":["direct"],"pingConfig":{}}`))
			transport.mu.Lock()
			transport.started, transport.release = make(chan struct{}, 1), make(chan struct{})
			started, release := transport.started, transport.release
			transport.mu.Unlock()
			chat, admin := &tgChat{ID: 9610 + int64(i), Type: "private"}, &tgUser{ID: 17}
			cb := &tgCallback{From: admin, Message: &tgMessage{MessageID: 1, Chat: chat}, Data: callback}
			s.handleCallback(cb)
			done := make(chan bool, 1)
			go func() {
				done <- s.handleBalancerInput(&tgMessage{From: admin, Chat: chat, Document: &tgDocument{FileID: "test-file"}})
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("download did not start")
			}
			cb.Data = "menu_main"
			s.handleCallback(cb)
			close(release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("download did not finish")
			}
			pendingRoutingChanges.Lock()
			defer pendingRoutingChanges.Unlock()
			for _, pending := range pendingRoutingChanges.items {
				if pending.ChatID == chat.ID {
					t.Fatal("cancelled download queued a confirmation")
				}
			}
		})
	}
}

func TestNativeAdvancedBalancingAcceptedByRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	for _, kind := range []string{"observatory", "burstObservatory"} {
		t.Run(kind, func(t *testing.T) {
			root := agentFixtureRoot(t)
			monitor, _ := decodeNetworkObject([]byte(`{"SubjectSelector":["direct"],"ProbeURL":"http://127.0.0.1/generate_204","ProbeInterval":"1m","EnableConcurrency":true}`))
			if kind == "burstObservatory" {
				monitor, _ = decodeNetworkObject([]byte(`{"SubjectSelector":["direct"],"PingConfig":{"Destination":"http://127.0.0.1/generate_204","Interval":"1m","Sampling":4,"Timeout":"5s","HttpMethod":"HEAD"}}`))
			}
			if _, err := applyObservatoryChange(root, observatoryChange{Kind: kind, Action: "put", Config: monitor}); err != nil {
				t.Fatal(err)
			}
			item, _ := decodeNetworkObject([]byte(`{"tag":"pool","selector":["direct"],"strategy":{"type":"leastLoad","settings":{"expected":1,"maxRTT":"2s","tolerance":0.3,"baselines":["100ms"],"costs":[{"match":"direct","value":1}]}}}`))
			if _, err := applyBalancerChange(root, balancerChange{Kind: "put", Tag: "pool", Balancer: item}); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(root)
			if output, err := exec.Command(bin, "run", "-test", "-config", writeRoutingConfig(t, string(data))).CombinedOutput(); err != nil {
				t.Fatalf("Xray rejected advanced %s: %v\n%s", kind, err, output)
			}
		})
	}
}
