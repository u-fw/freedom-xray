package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAgentNativeBalancingSingleExportRequiresSensitiveOptInAndCurrentReference(t *testing.T) {
	root := nativeNetworkExportFixture(t)
	fixture, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(fixture), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	for _, kind := range []string{"balancer", "monitor"} {
		operation := "balancers.get"
		_, etag := networkExportReference(t, root, kind)
		input := map[string]any{"tag": "pool", "etag": etag}
		if kind == "monitor" {
			operation, input = "observatories.get", map[string]any{"kind": "observatory", "etag": etag}
		}
		raw, _ := json.Marshal(input)
		var output bytes.Buffer
		if code := runAgentCLI([]string{operation}, bytes.NewReader(raw), &output); code != 3 || strings.Contains(output.String(), "original-export-secret") {
			t.Fatalf("single export was not gated: %s", output.String())
		}
		op, _ := findAgentOperation(operation)
		result, err := executeAgentOperation(cfg, op, raw, false, true)
		encoded, _ := json.Marshal(result)
		if err != nil || !bytes.Contains(encoded, []byte("original-export-secret")) || bytes.Contains(encoded, []byte(`"inbounds"`)) {
			t.Fatalf("single export lost its scope or native settings: %s %v", encoded, err)
		}
		input["etag"] = strings.Repeat("0", 24)
		raw, _ = json.Marshal(input)
		if _, err := executeAgentOperation(cfg, op, raw, false, true); err == nil {
			t.Fatal("single export accepted a stale reference")
		}
	}
	if after, _ := os.ReadFile(h.config); !bytes.Equal(after, fixture) || h.restartCount(t) != 0 {
		t.Fatal("read-only export modified the configuration")
	}
}
