package main

import (
	"net/url"
	"strings"
	"testing"
)

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

	uri := node.GenerateVlessURI(XrayClient{ID: "11111111-1111-1111-1111-111111111111", Email: "u@x"}, "203.0.113.9")
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
	if q.Get("host") != "example.com" || q.Get("sni") != "example.com" {
		t.Errorf("host/sni = %q/%q", q.Get("host"), q.Get("sni"))
	}
	if q.Get("path") != "/stream" || q.Get("mode") != "packet-up" {
		t.Errorf("path/mode = %q/%q", q.Get("path"), q.Get("mode"))
	}
	if q.Get("flow") != "" {
		t.Errorf("flow should be empty, got %q", q.Get("flow"))
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
