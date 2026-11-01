package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXrayHelperErrorDoesNotExposeSecretArguments(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := xrayBinPath
	xrayBinPath = bin
	defer func() { xrayBinPath = previous }()
	_, err := runXrayHelper("x25519", "-i", "test-private-key")
	if err == nil {
		t.Fatal("failing helper reported success")
	}
	if strings.Contains(err.Error(), "test-private-key") {
		t.Fatalf("helper error exposed a secret: %s", err)
	}
}

func TestGenerateVlessURIReality(t *testing.T) {
	derivationMu.Lock()
	derivationCache["x25519|PRIV"] = "PUBKEY"
	derivationCache["mldsa65|SEED"] = "VERIFYKEY"
	derivationMu.Unlock()

	node := &InboundDetails{
		Tag:           "node-443",
		Port:          443,
		Network:       "tcp",
		Security:      "reality",
		SNI:           "www.nvidia.com",
		PrivateKey:    "PRIV",
		MLDSA65Seed:   "SEED",
		ShortID:       "abcd",
		ClientEncrypt: "MLKEMCLIENT",
	}

	uri := node.GenerateVlessURI(XrayClient{ID: "11111111-1111-1111-1111-111111111111", Email: "u@x", Flow: "xtls-rprx-vision"}, "203.0.113.9")
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	q := u.Query()

	checks := map[string]string{
		"type":       "tcp",
		"security":   "reality",
		"pbk":        "PUBKEY",
		"pqv":        "VERIFYKEY",
		"sid":        "abcd",
		"spx":        "/",
		"flow":       "xtls-rprx-vision",
		"sni":        "www.nvidia.com",
		"encryption": "MLKEMCLIENT",
	}
	for key, want := range checks {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if u.Host != "203.0.113.9:443" {
		t.Errorf("host = %q", u.Host)
	}
	if !strings.Contains(u.Fragment, "node-443") {
		t.Errorf("fragment = %q", u.Fragment)
	}
}

func TestGenerateVlessURITLS(t *testing.T) {
	node := &InboundDetails{
		Tag:           "cdn",
		Port:          8443,
		Network:       "xhttp",
		Security:      "tls",
		SNI:           "example.com",
		XHTTPPath:     "/stream",
		XHTTPMode:     "packet-up",
		ClientEncrypt: "none",
	}

	uri := node.GenerateVlessURI(XrayClient{ID: "22222222-2222-2222-2222-222222222222", Email: "u@x"}, "203.0.113.9")
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	q := u.Query()

	if u.Host != "example.com:8443" {
		t.Errorf("host = %q", u.Host)
	}
	if q.Get("host") != "" || q.Get("sni") != "example.com" {
		t.Errorf("host/sni = %q/%q", q.Get("host"), q.Get("sni"))
	}
	if q.Get("alpn") != "h2" {
		t.Errorf("alpn = %q", q.Get("alpn"))
	}
	if q.Get("extra") != `{"xPaddingBytes":"100-1000"}` {
		t.Errorf("extra = %q", q.Get("extra"))
	}
	if q.Get("ech") != defaultXHTTPECHConfig {
		t.Errorf("ech = %q", q.Get("ech"))
	}
	if !strings.Contains(uri, "ech=cloudflare-ech.com%2Bhttps%3A%2F%2Fdns.alidns.com%2Fdns-query") {
		t.Errorf("ECH + / URI encoding incorrect: %s", uri)
	}
	if q.Get("path") != "/stream" || q.Get("mode") != "packet-up" {
		t.Errorf("path/mode = %q/%q", q.Get("path"), q.Get("mode"))
	}
	if q.Get("flow") != "" {
		t.Errorf("flow should be empty, got %q", q.Get("flow"))
	}
}

func TestGenerateVlessURITLSHostOverrideAndFlow(t *testing.T) {
	node := &InboundDetails{Tag: "cdn", Port: 443, Network: "xhttp", Security: "tls", SNI: "cdn.example.com", XHTTPHost: "authority.example.com", XHTTPPath: "/stream", ClientEncrypt: "TEST"}
	uri := node.GenerateVlessURI(XrayClient{ID: "22222222-2222-2222-2222-222222222222", Flow: "xtls-rprx-vision"}, "203.0.113.9")
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("host") != "authority.example.com" {
		t.Errorf("host override = %q", q.Get("host"))
	}
	if q.Get("flow") != "xtls-rprx-vision" {
		t.Errorf("flow = %q", q.Get("flow"))
	}
}

func TestGenerateVlessURICustomXHTTPPaddingAndRealityExclusion(t *testing.T) {
	node := &InboundDetails{Tag: "xhttp-reality", Port: 8443, Network: "xhttp", Security: "reality", SNI: "example.com", PublicKey: "testpub", XHTTPMode: "stream-up", XHTTPPaddingBytes: "200-900", ClientEncrypt: "none"}
	uri := node.GenerateVlessURI(XrayClient{ID: "33333333-3333-3333-3333-333333333333"}, "203.0.113.2")
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("extra") != `{"xPaddingBytes":"200-900"}` {
		t.Errorf("wrong extra: %q", q.Get("extra"))
	}
	if q.Get("mode") != "stream-up" {
		t.Errorf("lost flat mode: %q", q.Get("mode"))
	}
	if q.Get("ech") != "" {
		t.Errorf("ECH must not be injected into REALITY: %q", q.Get("ech"))
	}
}

func TestXHTTPTLSWithoutEncryptionDoesNotExportVision(t *testing.T) {
	node := &InboundDetails{
		Tag: "cdn", Port: 55555, PublicPort: 443, Network: "xhttp", Security: "tls",
		SNI: "cdn.example.com", XHTTPPath: "/stream", ClientEncrypt: "none",
	}
	uri := node.GenerateVlessURI(XrayClient{ID: "11111111-1111-1111-1111-111111111111", Flow: "xtls-rprx-vision"}, "203.0.113.3")
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Port() != "443" {
		t.Fatalf("public port not exported: %s", uri)
	}
	if parsed.Query().Get("flow") != "" {
		t.Fatal("unexpected Vision when VLESS Encryption is disabled")
	}
	if parsed.Query().Get("extra") != `{"xPaddingBytes":"100-1000"}` {
		t.Fatal("extra did not follow official structure")
	}
	if parsed.Query().Get("mode") != "auto" {
		t.Fatal("missing top-level mode")
	}
}

func TestGenerateVlessURIIPv6(t *testing.T) {
	node := &InboundDetails{
		Tag:      "v6",
		Port:     8443,
		Network:  "xhttp",
		Security: "reality",
		SNI:      "www.nvidia.com",
		ShortID:  "abcd",
	}

	uri := node.GenerateVlessURI(XrayClient{ID: "33333333-3333-3333-3333-333333333333"}, "2001:db8::1")
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	if u.Host != "[2001:db8::1]:8443" {
		t.Errorf("host = %q", u.Host)
	}
}

func TestDomainFromCertFile(t *testing.T) {
	cases := map[string]string{
		"/usr/local/etc/xray/certs/example.com.crt": "example.com",
		"/tmp/fullchain.pem":                        "fullchain",
		"":                                          "",
	}
	for in, want := range cases {
		if got := domainFromCertFile(in); got != want {
			t.Errorf("domainFromCertFile(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFieldAfterColon(t *testing.T) {
	out := "PrivateKey: priv\nPassword (PublicKey): pub\n"
	if got := fieldAfterColon(out, "Password", "PublicKey", "Public key", "Public"); got != "pub" {
		t.Errorf("public = %q", got)
	}
	mldsa := "Seed: aaa\nVerify: bbb\n"
	if got := strings.Join(strings.Fields(fieldAfterColon(mldsa, "Verify")), ""); got != "bbb" {
		t.Errorf("verify = %q", got)
	}
}
