package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func newBalancerOverrideHarness(t *testing.T) (*BotConfig, *bindingMigrationHarness) {
	t.Helper()
	root := agentFixtureRoot(t)
	root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool", "selector": []any{"direct"}, "strategy": map[string]any{"type": "roundRobin"}}}
	root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"tag": "direct2", "protocol": "freedom"})
	if _, err := applyRoutingService(root, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(fixture), false)
	script := `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$1 $2" in
  "api bi")
    if [ -f "$dir/status-failure" ]; then exit 1; fi
    target=$(cat "$dir/override-target" 2>/dev/null || true)
    printf '{"balancer":{"override":{"target":"%s"},"principleTarget":{"tag":["direct","direct2"]}}}\n' "$target"
    ;;
  "api bo")
    printf 'request\n' >> "$dir/override-requests"
    target=''
    shift 2
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -server|-timeout|-b) shift 2 ;;
        -r) target=''; shift ;;
        --) shift; target="$1"; shift ;;
        *) exit 2 ;;
      esac
    done
    if [ -f "$dir/ignore-override" ]; then exit 0; fi
    printf '%s' "$target" > "$dir/override-target"
    if [ -f "$dir/error-after-write" ]; then exit 1; fi
    ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(h.xray, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.AllowMutations = h.config, h.xray, true
	return cfg, &h
}

func overrideRequestCount(t *testing.T, cfg *BotConfig) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(cfg.XrayBinPath), "override-requests"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Count(string(data), "request\n")
}

func TestBalancerOverridePreviewApplyAndRestoreDoNotWriteConfiguration(t *testing.T) {
	cfg, h := newBalancerOverrideHarness(t)
	before, _ := os.ReadFile(h.config)
	initial, err := queryBalancerLive(cfg, "pool")
	if err != nil || len(initial.StateETag) != 24 || len(initial.OverrideTargets) != 2 {
		t.Fatalf("missing runtime references or targets: %+v %v", initial, err)
	}
	change := balancerOverrideChange{Tag: "pool", StateETag: initial.StateETag, Target: "direct2"}
	if _, err := setBalancerOverride(cfg, change, false); err != nil || overrideRequestCount(t, cfg) != 0 {
		t.Fatalf("preview changed runtime state: %v", err)
	}
	updated, err := setBalancerOverride(cfg, change, true)
	if err != nil || updated.Override != "direct2" || updated.StateETag == initial.StateETag || overrideRequestCount(t, cfg) != 1 {
		t.Fatalf("override did not apply: %+v %v", updated, err)
	}
	if _, err := setBalancerOverride(cfg, change, true); !errors.Is(err, errBalancerLiveChanged) || overrideRequestCount(t, cfg) != 1 {
		t.Fatalf("stale override was applied: %v", err)
	}
	for _, target := range []string{"api", "block", "missing"} {
		if _, err := setBalancerOverride(cfg, balancerOverrideChange{Tag: "pool", StateETag: updated.StateETag, Target: target}, true); err == nil {
			t.Fatalf("accepted a protected or unmatched target: %s", target)
		}
	}
	restored, err := setBalancerOverride(cfg, balancerOverrideChange{Tag: "pool", StateETag: updated.StateETag, Target: ""}, true)
	if err != nil || restored.Override != "" || overrideRequestCount(t, cfg) != 2 {
		t.Fatalf("automatic selection was not restored: %+v %v", restored, err)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("runtime control changed persistent configuration or restarted Xray")
	}
}

func TestBalancerOverrideRejectsChangedConfigurationAndReportsUncertainOutcomes(t *testing.T) {
	for _, mode := range []string{"config-change", "runtime-change", "ignore-override", "error-after-write"} {
		t.Run(mode, func(t *testing.T) {
			cfg, h := newBalancerOverrideHarness(t)
			status, err := queryBalancerLive(cfg, "pool")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "config-change" {
				root, _ := decodeBindingMigrationConfig(h.config)
				root["outbounds"].([]any)[0].(map[string]any)["settings"] = map[string]any{"domainStrategy": "UseIP"}
				data, _ := json.Marshal(root)
				if err := os.WriteFile(h.config, data, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				name, data := mode, []byte{}
				if mode == "runtime-change" {
					name, data = "override-target", []byte("direct")
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(h.xray), name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = setBalancerOverride(cfg, balancerOverrideChange{Tag: "pool", StateETag: status.StateETag, Target: "direct2"}, true)
			if err == nil {
				t.Fatal("unsafe or unverified override reported success")
			}
			if mode == "config-change" || mode == "runtime-change" {
				if !errors.Is(err, errBalancerLiveChanged) || overrideRequestCount(t, cfg) != 0 {
					t.Fatal("stale state was overwritten")
				}
			} else if overrideRequestCount(t, cfg) != 1 || !strings.Contains(err.Error(), "refresh live status") {
				t.Fatal("uncertain result retried or omitted the refresh instruction")
			}
		})
	}
}

func TestBotBalancerOverrideIsOwnerBoundAndReturnsToLiveStatus(t *testing.T) {
	cfg, h := newBalancerOverrideHarness(t)
	s, capture := newCapturedRoutingBot(t, cfg)
	chat, admin := &tgbotapi.Chat{ID: 9630, Type: "private"}, &tgbotapi.User{ID: 17}
	cb := &tgbotapi.CallbackQuery{From: admin, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}}
	status, _ := queryBalancerLive(cfg, "pool")
	root, _ := decodeBindingMigrationConfig(h.config)
	records, _ := balancerRecords(root)
	cb.Data = balancerCallback("override", "pool", records[0].ETag)
	s.handleCallback(cb)
	_, markup := capture.card(t)
	buttons := strings.Join(keyboardCallbacks(t, markup), " ")
	selectTarget := balancerOverrideCallback("op", "pool", status.StateETag, userCallbackKey("direct2"))
	if !strings.Contains(buttons, selectTarget) {
		t.Fatal("runtime target picker omitted a matching outbound")
	}
	cb.Data = selectTarget
	s.handleCallback(cb)
	if overrideRequestCount(t, cfg) != 0 {
		t.Fatal("target selection applied without confirmation")
	}
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cb.From = &tgbotapi.User{ID: 18}
	s.resolveRoutingChange(cb, true)
	if overrideRequestCount(t, cfg) != 0 {
		t.Fatal("another administrator applied a runtime change")
	}
	cb.From = admin
	s.resolveRoutingChange(cb, true)
	body, markup := capture.card(t)
	if overrideRequestCount(t, cfg) != 1 || !strings.Contains(body, "direct2") || !strings.Contains(strings.Join(keyboardCallbacks(t, markup), " "), "bl:override:") {
		t.Fatal("confirmed runtime change did not return to refreshed live status")
	}
	s.resolveRoutingChange(cb, true)
	if overrideRequestCount(t, cfg) != 1 || h.restartCount(t) != 0 {
		t.Fatal("runtime confirmation was replayed or restarted Xray")
	}
	// A choice made before another runtime switch must not create a confirmation.
	cb.Data = selectTarget
	s.handleCallback(cb)
	pendingRoutingChanges.Lock()
	for _, pending := range pendingRoutingChanges.items {
		if pending.ChatID == chat.ID {
			pendingRoutingChanges.Unlock()
			t.Fatal("stale runtime picker queued a confirmation")
		}
	}
	pendingRoutingChanges.Unlock()
	status, _ = queryBalancerLive(cfg, "pool")
	cb.Data = balancerOverrideCallback("op", "pool", status.StateETag, "auto")
	s.handleCallback(cb)
	cb.Data = "routing_confirm:" + pendingRoutingToken(t, chat.ID)
	cfg.AllowMutations = false
	s.resolveRoutingChange(cb, true)
	if overrideRequestCount(t, cfg) != 1 {
		t.Fatal("revoked mutation permission was ignored")
	}
}

func TestBotBalancerOverrideConfirmationExpiresAndCancelsWithoutMutation(t *testing.T) {
	for i, action := range []string{"cancel", "expire", "runtime-change"} {
		t.Run(action, func(t *testing.T) {
			cfg, h := newBalancerOverrideHarness(t)
			s, _ := newCapturedRoutingBot(t, cfg)
			status, _ := queryBalancerLive(cfg, "pool")
			chat := &tgbotapi.Chat{ID: 9631 + int64(i), Type: "private"}
			cb := &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 17}, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: balancerOverrideCallback("op", "pool", status.StateETag, userCallbackKey("direct2"))}
			s.handleCallback(cb)
			key := pendingRoutingToken(t, chat.ID)
			cb.Data = "routing_confirm:" + key
			if action == "expire" {
				pendingRoutingChanges.Lock()
				pending := pendingRoutingChanges.items[key]
				pending.Expires = time.Now().Add(-time.Second)
				pendingRoutingChanges.items[key] = pending
				pendingRoutingChanges.Unlock()
			} else if action == "runtime-change" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(h.xray), "override-target"), []byte("direct"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.resolveRoutingChange(cb, action != "cancel")
			if overrideRequestCount(t, cfg) != 0 || h.restartCount(t) != 0 {
				t.Fatal("cancelled, expired or stale confirmation changed runtime state")
			}
		})
	}
}

func TestAgentBalancerOverrideRequiresExplicitTargetAndPreviewsBeforeApply(t *testing.T) {
	cfg, _ := newBalancerOverrideHarness(t)
	op, ok := findAgentOperation("balancers.override")
	if !ok || !op.Mutation || !op.Input {
		t.Fatal("runtime override is missing its discoverable mutation contract")
	}
	for _, raw := range []string{`{"tag":"pool","state_etag":"invalid"}`, `{"tag":"pool","target":null}`, `{"tag":"pool","target":"direct","other":true}`} {
		if _, err := executeAgentOperation(cfg, op, []byte(raw), false, false); err == nil {
			t.Fatalf("accepted an incomplete override request: %s", raw)
		}
	}
	status, _ := queryBalancerLive(cfg, "pool")
	input, _ := json.Marshal(balancerOverrideChange{Tag: "pool", StateETag: status.StateETag, Target: "direct2"})
	preview, err := executeAgentOperation(cfg, op, input, false, false)
	if err != nil || preview.(map[string]any)["applied"] != false || overrideRequestCount(t, cfg) != 0 {
		t.Fatalf("agent preview mutated runtime state: %v", err)
	}
	result, err := executeAgentOperation(cfg, op, input, true, false)
	if err != nil || result.(map[string]any)["applied"] != true || overrideRequestCount(t, cfg) != 1 {
		t.Fatalf("agent apply did not update runtime state: %v", err)
	}
}

func TestBalancerOverridePickerPaginationKeepsReviewedStateAndCallbackLimits(t *testing.T) {
	cfg, h := newBalancerOverrideHarness(t)
	root, _ := decodeBindingMigrationConfig(h.config)
	for i := 0; i < 2*routingPageSize; i++ {
		root["outbounds"] = append(root["outbounds"].([]any), map[string]any{"tag": fmt.Sprintf("direct-extra-%d", i), "protocol": "freedom"})
	}
	data, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	s, capture := newCapturedRoutingBot(t, cfg)
	status, _ := queryBalancerLive(cfg, "pool")
	chat := &tgbotapi.Chat{ID: 9635, Type: "private"}
	cb := &tgbotapi.CallbackQuery{From: &tgbotapi.User{ID: 17}, Message: &tgbotapi.Message{MessageID: 1, Chat: chat}, Data: balancerOverrideCallback("ol", "pool", status.StateETag, "0")}
	s.handleCallback(cb)
	_, markup := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, markup), " ")
	next := balancerOverrideCallback("ol", "pool", status.StateETag, fmt.Sprint(routingPageSize))
	if !strings.Contains(callbacks, next) {
		t.Fatal("runtime picker omitted its next page")
	}
	cb.Data = next
	s.handleCallback(cb)
	_, markup = capture.card(t)
	callbacks = strings.Join(keyboardCallbacks(t, markup), " ")
	if !strings.Contains(callbacks, balancerOverrideCallback("ol", "pool", status.StateETag, "0")) || overrideRequestCount(t, cfg) != 0 {
		t.Fatal("pagination lost its state reference or applied a runtime change")
	}
}
