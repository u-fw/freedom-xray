package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestStatsRepairRejectsPublicOrReferencedAPI(t *testing.T) {
	for _, configure := range []func(map[string]any){
		func(root map[string]any) {
			root["outbounds"] = []any{map[string]any{"tag": "api", "protocol": "freedom"}}
		},
		func(root map[string]any) { root["api"] = map[string]any{"tag": "api", "listen": "0.0.0.0:10085"} },
		func(root map[string]any) {
			root["api"] = map[string]any{"tag": "api", "listen": "127.0.0.1:10085"}
			root["inbounds"] = append(root["inbounds"].([]any), map[string]any{
				"tag": "api-internal", "listen": "0.0.0.0", "port": json.Number("10086"),
				"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"},
			})
			root["routing"].(map[string]any)["rules"] = []any{map[string]any{"type": "field", "inboundTag": []any{"api-internal"}, "outboundTag": "api"}}
		},
		func(root map[string]any) {
			root["api"] = map[string]any{"tag": "api"}
			root["routing"].(map[string]any)["rules"] = []any{map[string]any{"network": "tcp", "outboundTag": "api"}}
		},
		func(root map[string]any) {
			root["api"] = map[string]any{"tag": "api"}
			root["routing"].(map[string]any)["balancers"] = []any{map[string]any{"tag": "pool", "selector": []any{"a"}}}
		},
	} {
		root := agentFixtureRoot(t)
		configure(root)
		before, _ := json.Marshal(root)
		if _, err := applyStatsInfrastructure(root, "127.0.0.1:10085"); err == nil {
			t.Fatal("accepted unsafe API exposure")
		}
		after, _ := json.Marshal(root)
		if !bytes.Equal(before, after) {
			t.Fatal("unsafe repair partially modified the candidate")
		}
	}
}

func TestStatsRepairSupportsSafeDirectAPI(t *testing.T) {
	root := agentFixtureRoot(t)
	root["api"] = map[string]any{"tag": "api", "listen": "127.0.0.1:10085"}
	changed, err := applyStatsInfrastructure(root, "127.0.0.1:10085")
	if err != nil || !changed {
		t.Fatalf("direct API setup failed: %v %v", changed, err)
	}
	inbounds := root["inbounds"].([]any)
	if len(inbounds) != 1 {
		t.Fatal("added a conflicting API inbound")
	}
	data, _ := json.Marshal(root)
	path := writeRoutingConfig(t, string(data))
	report, err := inspectStatsSwitches(path, "127.0.0.1:10085")
	if err != nil || len(report.APIMissing) != 0 {
		t.Fatalf("safe direct API reported missing: %+v %v", report, err)
	}
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected direct API: %v\n%s", err, output)
	}
}

func TestStatsInspectionReportsPublicAPIListener(t *testing.T) {
	root := agentFixtureRoot(t)
	root["api"] = map[string]any{"tag": "api", "listen": "0.0.0.0:10085", "services": []any{"StatsService"}}
	data, _ := json.Marshal(root)
	report, err := inspectStatsSwitches(writeRoutingConfig(t, string(data)), "127.0.0.1:10085")
	if err != nil || !strings.Contains(strings.Join(report.APIMissing, ","), "loopback") {
		t.Fatalf("public API not reported: %+v %v", report, err)
	}
}

func TestStatsInspectionAcceptsSafeDirectAndInternalListeners(t *testing.T) {
	root := agentFixtureRoot(t)
	root["api"] = map[string]any{"tag": "api", "listen": "127.0.0.1:10085"}
	root["inbounds"] = append(root["inbounds"].([]any), map[string]any{
		"tag": "api-internal", "listen": "127.0.0.1", "port": json.Number("10086"),
		"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"},
	})
	if _, err := applyStatsInfrastructure(root, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(root)
	report, err := inspectStatsSwitches(writeRoutingConfig(t, string(data)), "127.0.0.1:10085")
	if err != nil || len(report.APIMissing) != 0 {
		t.Fatalf("safe listeners reported as unsafe: %+v %v", report, err)
	}
}
