package main

import (
	"strings"
	"testing"
)

func TestNormalizeServerHost(t *testing.T) {
	valid := map[string]string{
		"203.0.113.7":      "203.0.113.7",
		"example.com":      "example.com",
		"vpn.example.com.": "vpn.example.com",
		"[2001:db8::1]":    "2001:db8::1",
		"2001:db8::1":      "2001:db8::1",
	}
	for input, want := range valid {
		got, err := normalizeServerHost(input)
		if err != nil {
			t.Errorf("normalizeServerHost(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeServerHost(%q)=%q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"127.0.0.1", "::1", "0.0.0.0", "https://example.com", "example.com:443", "bad host"} {
		if got, err := normalizeServerHost(input); err == nil {
			t.Errorf("normalizeServerHost(%q) accepted %q", input, got)
		}
	}
}

func TestResolveServerHostUsesExplicitConfig(t *testing.T) {
	host, err := resolveServerHost(&BotConfig{ServerHost: "vpn.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if host != "vpn.example.com" {
		t.Fatalf("explicit server_host ignored: %q", host)
	}
	invalid, err := resolveServerHost(&BotConfig{ServerHost: "127.0.0.1"})
	if err == nil {
		t.Fatalf("loopback server_host accepted: %q", invalid)
	}
}

func TestRealityExportRejectsLoopbackHost(t *testing.T) {
	item := userExportEntry{
		Node:   InboundDetails{Tag: "example", Port: 443, Protocol: "vless", Network: "tcp", Security: "reality", SNI: "www.nvidia.com", PublicKey: "test-public-key", ShortID: "abcd", SpiderX: "/"},
		Client: XrayClient{ID: "11111111-1111-4111-8111-111111111111", Email: "phone"},
	}
	if _, err := validatedUserExportURI(item, "127.0.0.1"); err == nil || !strings.Contains(err.Error(), "server address") {
		t.Fatalf("loopback REALITY host was not rejected: %v", err)
	}
	uri, err := validatedUserExportURI(item, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uri, "@203.0.113.7:443") {
		t.Fatalf("valid host missing from URI: %s", uri)
	}
}
