package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestAPISafetyRejectsAliasesBeforeRepairOrRuntimeRequests(t *testing.T) {
	internal := func(root map[string]any) map[string]any {
		for _, value := range root["inbounds"].([]any) {
			item := value.(map[string]any)
			if item["tag"] == "api-internal" {
				return item
			}
		}
		t.Fatal("API scaffold is missing its internal inbound")
		return nil
	}
	for index, mutate := range []func(map[string]any){
		func(root map[string]any) { root["api"].(map[string]any)["Listen"] = "0.0.0.0:19000" },
		func(root map[string]any) { root["API"] = map[string]any{"listen": "0.0.0.0:19000"} },
		func(root map[string]any) { root["api"].(map[string]any)["Tag"] = "another-api" },
		func(root map[string]any) {
			root["routing"].(map[string]any)["rules"] = append(root["routing"].(map[string]any)["rules"].([]any), map[string]any{"type": "field", "OutboundTag": "api", "network": "tcp"})
		},
		func(root map[string]any) {
			root["routing"].(map[string]any)["Rules"] = []any{map[string]any{"outboundTag": "api", "network": "tcp"}}
		},
		func(root map[string]any) {
			root["routing"].(map[string]any)["balancers"].([]any)[0].(map[string]any)["Selector"] = []any{"api"}
		},
		func(root map[string]any) {
			root["routing"].(map[string]any)["balancers"].([]any)[0].(map[string]any)["FallbackTag"] = "api"
		},
		func(root map[string]any) { internal(root)["Listen"] = "0.0.0.0" },
		func(root map[string]any) {
			internal(root)["settings"].(map[string]any)["Address"] = "0.0.0.0"
		},
		func(root map[string]any) { root["outbounds"].([]any)[0].(map[string]any)["Tag"] = "api" },
	} {
		cfg, h := newBalancerOverrideHarness(t)
		status, err := queryBalancerLive(cfg, "pool")
		if err != nil {
			t.Fatal(err)
		}
		root, _ := decodeBindingMigrationConfig(h.config)
		mutate(root)
		before, _ := json.Marshal(root)
		if _, err := applyStatsInfrastructure(root, cfg.StatsAPIServer); err == nil {
			t.Fatalf("case %d: repair accepted a field alias hidden from API safety checks", index)
		}
		after, _ := json.Marshal(root)
		if !bytes.Equal(before, after) {
			t.Fatal("rejected API repair partially mutated its input")
		}
		if err := os.WriteFile(h.config, before, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := setBalancerOverride(cfg, balancerOverrideChange{Tag: "pool", StateETag: status.StateETag, Target: "direct2"}, true); err == nil || overrideRequestCount(t, cfg) != 0 {
			t.Fatal("unsafe API configuration reached runtime mutation")
		}
	}
}
