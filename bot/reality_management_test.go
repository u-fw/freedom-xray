package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func realityTestConfig(t *testing.T, security string, names []string, target, dest string) map[string]any {
	t.Helper()
	root := map[string]any{}
	inbound := map[string]any{
		"tag": "TCP-REALITY-443", "port": float64(443), "protocol": "vless",
		"settings": map[string]any{"clients": []any{}},
		"streamSettings": map[string]any{
			"network": "tcp", "security": security,
			"realitySettings": map[string]any{"serverNames": names, "target": target, "dest": dest, "privateKey": "private", "shortIds": []string{"abcd"}},
		},
	}
	root["inbounds"] = []any{inbound}
	// Ensure test uses the same untyped JSON shapes produced by the real decoder.
	serialized, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err = json.Unmarshal(serialized, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func realitySettingsFromRoot(t *testing.T, root map[string]any) map[string]any {
	t.Helper()
	inbounds := root["inbounds"].([]any)
	stream := inbounds[0].(map[string]any)["streamSettings"].(map[string]any)
	return stream["realitySettings"].(map[string]any)
}

func TestManagedRealitySNIStandardTarget(t *testing.T) {
	root := realityTestConfig(t, "reality", []string{"www.nvidia.com"}, "www.nvidia.com:443", "www.nvidia.com:443")
	result, err := applyManagedNodeChange(root, NodeChange{Kind: "reality_sni", Tag: "TCP-REALITY-443", Value: "www.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	got := realitySettingsFromRoot(t, root)
	if got["target"] != "www.example.com:443" || got["dest"] != "www.example.com:443" {
		t.Fatalf("standard targets not synchronized: %#v", got)
	}
	if got["serverNames"].([]any)[0] != "www.example.com" {
		t.Fatalf("serverNames missing: %#v", got)
	}
	if !strings.Contains(result, "SNI") {
		t.Fatal("missing feedback", result)
	}
}

func TestManagedRealitySNIPreservesCustomTarget(t *testing.T) {
	root := realityTestConfig(t, "reality", []string{"www.nvidia.com"}, "cdn.internal:9443", "other.internal:8443")
	if _, err := applyManagedNodeChange(root, NodeChange{Kind: "reality_sni", Tag: "TCP-REALITY-443", Value: "www.example.com"}); err != nil {
		t.Fatal(err)
	}
	got := realitySettingsFromRoot(t, root)
	if got["target"] != "cdn.internal:9443" || got["dest"] != "other.internal:8443" {
		t.Fatalf("custom targets clobbered: %#v", got)
	}
}

func TestManagedRealitySNIRejectsUnsafeOrAmbiguous(t *testing.T) {
	invalid := []string{"", "example", "bad..com", "-bad.com", "bad-.com", "https://example.com", "example.com:443", "192.0.2.1", "example.com.", "foo_bar.com"}
	for _, value := range invalid {
		if err := validateRealitySNI(value); err == nil {
			t.Errorf("accepted unsafe SNI %q", value)
		}
	}
	multi := realityTestConfig(t, "reality", []string{"a.example.com", "b.example.com"}, "a.example.com:443", "a.example.com:443")
	if _, err := applyManagedNodeChange(multi, NodeChange{Kind: "reality_sni", Tag: "TCP-REALITY-443", Value: "new.example.com"}); err == nil {
		t.Fatal("allowed ambiguous multi-SNI update")
	}
	root := realityTestConfig(t, "reality", []string{"www.nvidia.com"}, "www.nvidia.com:443", "www.nvidia.com:443")
	if _, err := applyManagedNodeChange(root, NodeChange{Kind: "mode", Tag: "TCP-REALITY-443", Value: "auto"}); err == nil {
		t.Fatal("allowed XHTTP mode on TCP REALITY")
	}
	rootTLS := realityTestConfig(t, "tls", []string{"www.nvidia.com"}, "www.nvidia.com:443", "www.nvidia.com:443")
	if _, err := applyManagedNodeChange(rootTLS, NodeChange{Kind: "reality_sni", Tag: "TCP-REALITY-443", Value: "new.example.com"}); err == nil {
		t.Fatal("allowed REALITY SNI on TLS")
	}
}

func TestBotXHTTPExtraPaddingPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	raw := `{"inbounds":[{"tag":"cdn","port":55555,"protocol":"vless","settings":{"clients":[{"email":"user","id":"11111111-1111-1111-1111-111111111111"}]},"streamSettings":{"network":"xhttp","security":"tls","tlsSettings":{"serverName":"cdn.example.com"},"xhttpSettings":{"path":"/stream","mode":"auto","xPaddingBytes":"100-1000","extra":{"xPaddingBytes":"200-900"}}}}]}`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	nodes, err := LoadXrayInbounds(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].XHTTPPaddingBytes != "200-900" {
		t.Fatalf("nested extra was not preferred: %#v", nodes)
	}
	uri := nodes[0].GenerateVlessURI(nodes[0].Clients[0], "203.0.113.4")
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if extra := parsed.Query().Get("extra"); extra != `{"xPaddingBytes":"200-900"}` {
		t.Fatalf("wrong exported extra: %s", extra)
	}
	if parsed.Query().Get("mode") != "auto" {
		t.Fatal("mode missing")
	}
}
