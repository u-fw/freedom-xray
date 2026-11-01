package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func templateTestParams(template NodeTemplate) NodeTemplateParams {
	p := NodeTemplateParams{
		Tag:          "TEST-NODE-443",
		Port:         443,
		SNI:          "www.example.com",
		PrivateKey:   "TESTONLY-private-key-material",
		ShortID:      "0123456789abcdef",
		Path:         "/stream-test",
		Mode:         "auto",
		XHTTPPadding: "100-1000",
		Decryption:   "none",
	}
	if template == NodeTemplateTCPReality {
		p.Vision = true
	}
	if template == NodeTemplateXHTTPTLS {
		p.CertFile = "/etc/freedom/certs/www.example.com.crt"
		p.KeyFile = "/etc/freedom/certs/www.example.com.key"
	}
	return p
}

func TestBuildTCPRealityInbound(t *testing.T) {
	p := templateTestParams(NodeTemplateTCPReality)
	inbound, err := BuildNodeInbound(NodeTemplateTCPReality, p)
	if err != nil {
		t.Fatal(err)
	}
	if clients, ok := inbound["settings"].(map[string]any)["clients"].([]any); !ok || len(clients) != 0 {
		t.Fatalf("new inbound must have no clients: %#v", inbound["settings"])
	}
	stream := inbound["streamSettings"].(map[string]any)
	if stream["network"] != "tcp" || stream["security"] != "reality" {
		t.Fatalf("bad stream: %#v", stream)
	}
	reality := stream["realitySettings"].(map[string]any)
	if reality["privateKey"] != p.PrivateKey {
		t.Fatal("private key not applied")
	}
	names := reality["serverNames"].([]any)
	if len(names) != 1 || names[0] != p.SNI {
		t.Fatalf("bad serverNames: %#v", names)
	}
	if reality["target"] != p.SNI+":443" || reality["dest"] != p.SNI+":443" {
		t.Fatalf("bad target/dest: %#v", reality)
	}
	if _, ok := reality["mldsa65Seed"]; ok {
		t.Fatal("ML-DSA seed must be omitted when empty")
	}
	sockopt := stream["sockopt"].(map[string]any)
	if sockopt["tcpFastOpen"] != true {
		t.Fatal("TCP fast open missing")
	}
	if _, ok := sockopt["trustedXForwardedFor"]; ok {
		t.Fatal("REALITY TCP must not trust XFF")
	}
}

func TestBuildXHTTPRealityInbound(t *testing.T) {
	inbound, err := BuildNodeInbound(NodeTemplateXHTTPReality, templateTestParams(NodeTemplateXHTTPReality))
	if err != nil {
		t.Fatal(err)
	}
	stream := inbound["streamSettings"].(map[string]any)
	if stream["network"] != "xhttp" || stream["security"] != "reality" {
		t.Fatalf("bad stream: %#v", stream)
	}
	xhttp := stream["xhttpSettings"].(map[string]any)
	if xhttp["path"] != "/stream-test" || xhttp["mode"] != "auto" {
		t.Fatalf("bad xhttp settings: %#v", xhttp)
	}
	extra, ok := xhttp["extra"].(map[string]any)
	if !ok || extra["xPaddingBytes"] != "100-1000" {
		t.Fatalf("padding must use nested extra: %#v", xhttp)
	}
	if _, legacy := xhttp["xPaddingBytes"]; legacy {
		t.Fatal("legacy top-level padding written")
	}
}

func TestBuildXHTTPTLSInbound(t *testing.T) {
	p := templateTestParams(NodeTemplateXHTTPTLS)
	inbound, err := BuildNodeInbound(NodeTemplateXHTTPTLS, p)
	if err != nil {
		t.Fatal(err)
	}
	stream := inbound["streamSettings"].(map[string]any)
	if stream["network"] != "xhttp" || stream["security"] != "tls" {
		t.Fatalf("bad stream: %#v", stream)
	}
	tlsSettings := stream["tlsSettings"].(map[string]any)
	if tlsSettings["serverName"] != p.SNI {
		t.Fatalf("bad TLS serverName: %#v", tlsSettings)
	}
	certs := tlsSettings["certificates"].([]any)
	cert := certs[0].(map[string]any)
	if cert["certificateFile"] != p.CertFile || cert["keyFile"] != p.KeyFile {
		t.Fatalf("bad cert entry: %#v", cert)
	}
	alpn := tlsSettings["alpn"].([]any)
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatalf("bad ALPN: %#v", alpn)
	}
	sockopt := stream["sockopt"].(map[string]any)
	xff := sockopt["trustedXForwardedFor"].([]any)
	if len(xff) != 1 || xff[0] != "CF-Connecting-IP" {
		t.Fatalf("bad trusted XFF: %#v", xff)
	}
}

func TestBuildNodeInboundValidation(t *testing.T) {
	cases := []struct {
		name     string
		template NodeTemplate
		mutate   func(*NodeTemplateParams)
	}{
		{"tag with plus", NodeTemplateTCPReality, func(p *NodeTemplateParams) { p.Tag = "bad+tag" }},
		{"tag with slash", NodeTemplateTCPReality, func(p *NodeTemplateParams) { p.Tag = "bad/tag" }},
		{"port zero", NodeTemplateTCPReality, func(p *NodeTemplateParams) { p.Port = 0 }},
		{"port too high", NodeTemplateTCPReality, func(p *NodeTemplateParams) { p.Port = 65536 }},
		{"invalid sni", NodeTemplateTCPReality, func(p *NodeTemplateParams) { p.SNI = "localhost" }},
		{"missing reality key", NodeTemplateTCPReality, func(p *NodeTemplateParams) { p.PrivateKey = "" }},
		{"bad short id", NodeTemplateTCPReality, func(p *NodeTemplateParams) { p.ShortID = "xyz" }},
		{"bad path", NodeTemplateXHTTPReality, func(p *NodeTemplateParams) { p.Path = "stream" }},
		{"bad mode", NodeTemplateXHTTPReality, func(p *NodeTemplateParams) { p.Mode = "turbo" }},
		{"bad padding", NodeTemplateXHTTPReality, func(p *NodeTemplateParams) { p.XHTTPPadding = "900-100" }},
		{"missing cert", NodeTemplateXHTTPTLS, func(p *NodeTemplateParams) { p.CertFile = "" }},
		{"missing key", NodeTemplateXHTTPTLS, func(p *NodeTemplateParams) { p.KeyFile = "" }},
		{"bad alpn", NodeTemplateXHTTPTLS, func(p *NodeTemplateParams) { p.ALPN = []string{"h3"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := templateTestParams(tc.template)
			tc.mutate(&p)
			if _, err := BuildNodeInbound(tc.template, p); err == nil {
				t.Fatal("invalid parameters accepted")
			}
		})
	}
}

func writeTestCertificate(t *testing.T, dir, domain string) (string, string) {
	t.Helper()
	certPEM, keyPEM, err := GenerateSelfSignedCertificatePEM(domain)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, domain+".crt")
	keyPath := filepath.Join(dir, domain+".key")
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestSelfSignedCertificatePair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestCertificate(t, dir, "www.example.com")
	if err := ValidateCertificatePair(certPath, keyPath, "www.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCertificatePair(certPath, keyPath, "other.example.com"); err == nil {
		t.Fatal("certificate accepted for uncovered hostname")
	}
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range cert.DNSNames {
		if name == "www.example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SAN missing: %#v", cert.DNSNames)
	}
	if cert.NotAfter.Before(cert.NotBefore.AddDate(9, 0, 0)) {
		t.Fatal("self-signed certificate validity is too short")
	}
	if _, _, err := GenerateSelfSignedCertificatePEM("localhost"); err == nil {
		t.Fatal("single-label domain accepted")
	}
}

func TestMetaClientEncryptionSidecar(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	sidecar := metaSetClientEncryptionSidecar(configPath, "node-a", "enc-value")
	data, err := sidecar.Transform(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	nodes := root["nodes"].(map[string]any)
	entry := nodes["node-a"].(map[string]any)
	if entry["client_encryption"] != "enc-value" {
		t.Fatalf("bad meta entry: %#v", entry)
	}

	remove := metaSetClientEncryptionSidecar(configPath, "node-a", "")
	data, err = remove.Transform(data, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if len(root["nodes"].(map[string]any)) != 0 {
		t.Fatalf("entry not removed: %#v", root)
	}
	missing, err := metaSetClientEncryptionSidecar(configPath, "node-a", "").Transform(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("absent meta file must stay absent, got %q", missing)
	}
}

func TestGeneratedCertificateNeverOverwritesAFileCreatedAfterStaging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "certificate.pem")
	staged, err := stageConfigSidecars([]configSidecar{fileSidecar(path, 0600, []byte("new"))})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = writeSidecar(staged[0]); err == nil {
		t.Fatal("created file was overwritten after staging")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("existing file changed: %q", data)
	}
	if _, err = stageConfigSidecars([]configSidecar{fileSidecar(path, 0600, []byte("new"))}); err == nil {
		t.Fatal("existing certificate was accepted during staging")
	}
}

func TestPublicPortsDeleteSidecar(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	sidecar := publicPortsDeleteSidecar(configPath, "node-a")
	data, err := sidecar.Transform(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Fatalf("absent public-port file must stay absent, got %q", data)
	}
	existing := []byte(`{"nodes":{"node-a":{"listen_port":443,"public_port":8443},"node-b":{"listen_port":80,"public_port":8080}}}`)
	data, err = sidecar.Transform(existing, true)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	nodes := root["nodes"].(map[string]any)
	if _, ok := nodes["node-a"]; ok {
		t.Fatalf("target entry not removed: %#v", nodes)
	}
	if _, ok := nodes["node-b"]; !ok {
		t.Fatalf("unrelated entry removed: %#v", nodes)
	}
}

func TestBuildNodeInboundDoesNotShareClientSlice(t *testing.T) {
	first, err := BuildNodeInbound(NodeTemplateTCPReality, templateTestParams(NodeTemplateTCPReality))
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildNodeInbound(NodeTemplateTCPReality, templateTestParams(NodeTemplateTCPReality))
	if err != nil {
		t.Fatal(err)
	}
	clients := first["settings"].(map[string]any)["clients"].([]any)
	clients = append(clients, map[string]any{"id": "x", "email": "y"})
	first["settings"].(map[string]any)["clients"] = clients
	if got := len(second["settings"].(map[string]any)["clients"].([]any)); got != 0 {
		t.Fatalf("builder leaked client state across calls: %d", got)
	}
}

func TestNodeTemplateParamsNormalize(t *testing.T) {
	p := normalizedNodeParams(NodeTemplateParams{})
	if p.Mode != "auto" || p.XHTTPPadding != "100-1000" || p.Decryption != "none" {
		t.Fatalf("unexpected defaults: %+v", p)
	}
	if len(p.ALPN) != 2 || len(p.TrustedXForwardedFor) != 1 {
		t.Fatalf("missing defaults: %+v", p)
	}
	if p.MaxTimeDiff != defaultNodeMaxTimeDiff {
		t.Fatalf("maxTimeDiff default: %+v", p)
	}
	if !strings.Contains(p.Email, "template") {
		t.Fatalf("placeholder email: %+v", p)
	}
}
