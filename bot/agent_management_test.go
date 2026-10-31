package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const agentFixture = `{
 "inbounds":[{"tag":"raw","listen":"127.0.0.1","port":28443,"protocol":"vless","settings":{"decryption":"none","clients":[{"id":"11111111-1111-4111-8111-111111111111","email":"alice","level":3}]}}],
 "outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"}],
 "routing":{"rules":[]},"policy":{"levels":{"3":{"handshake":5}}}
}`

func agentFixtureRoot(t *testing.T) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(agentFixture))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAgentContractAndSensitiveOutputGate(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		input string
		exit  int
		code  string
	}{
		{[]string{"capabilities"}, "", 0, ""},
		{[]string{"version"}, "", 0, ""},
		{[]string{"does.not.exist"}, "", 2, "unknown_operation"},
		{[]string{"users.export"}, `{"user":"alice"}`, 3, "permission_denied"},
		{[]string{"nodes.get"}, "", 2, "invalid_request"},
		{[]string{"version", "--apply"}, "", 2, "invalid_request"},
	} {
		var out bytes.Buffer
		if got := runAgentCLI(tc.args, strings.NewReader(tc.input), &out); got != tc.exit {
			t.Fatalf("%v exit %d: %s", tc.args, got, out.String())
		}
		var response agentResponse
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.SchemaVersion != 1 || response.OK != (tc.exit == 0) {
			t.Fatalf("bad envelope: %s", out.String())
		}
		if tc.code != "" && (response.Error == nil || response.Error.Code != tc.code) {
			t.Fatalf("bad error: %s", out.String())
		}
	}
	var value agentNameInput
	for _, input := range []string{`{"user":"alice","surprise":true}`, `{"user":"alice"}{}`, `null`} {
		if err := decodeAgentInput([]byte(input), &value); err == nil {
			t.Fatalf("accepted input %s", input)
		}
	}
}

func TestAgentPreviewDoesNotWriteAndApplyHidesUUID(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	cfg.XrayBinPath = h.xray
	op, _ := findAgentOperation("users.add")
	input := []byte(`{"user":"bob","node":"raw"}`)
	before, err := os.ReadFile(h.config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeAgentOperation(cfg, op, input, false, false)
	if err != nil {
		t.Fatal(err)
	}
	preview, _ := json.Marshal(result)
	after, err := os.ReadFile(h.config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 || !bytes.Contains(preview, []byte(`"applied":false`)) {
		t.Fatal("preview changed state")
	}
	result, err = executeAgentOperation(cfg, op, input, true, false)
	if err != nil {
		t.Fatal(err)
	}
	applied, _ := json.Marshal(result)
	if bytes.Contains(applied, []byte("11111111-1111")) || bytes.Contains(applied, []byte("uuid")) {
		t.Fatalf("credentials leaked: %s", applied)
	}
	if h.restartCount(t) != 1 {
		t.Fatalf("expected one restart, got %d", h.restartCount(t))
	}
	users, err := agentUsers(h.config)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, user := range users {
		if user.User == "bob" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new user absent: %+v", users)
	}
}

func TestStatsScaffoldLoopbackAndIdempotence(t *testing.T) {
	root := agentFixtureRoot(t)
	changed, err := applyStatsInfrastructure(root, "127.0.0.1:10085")
	if err != nil || !changed {
		t.Fatalf("scaffold: changed=%v err=%v", changed, err)
	}
	routing := root["routing"].(map[string]any)
	rules := routing["rules"].([]any)
	if !isInternalAPIRoute(rules[0].(map[string]any), "api") {
		t.Fatal("internal API route is not first")
	}
	levels := root["policy"].(map[string]any)["levels"].(map[string]any)
	if levels["3"].(map[string]any)["statsUserUplink"] != true {
		t.Fatal("number-valued client level was not enabled")
	}
	changed, err = applyStatsInfrastructure(root, "127.0.0.1:10085")
	if err != nil || changed {
		t.Fatalf("scaffold was not idempotent: %v %v", changed, err)
	}
	for _, server := range []string{"0.0.0.0:10085", "8.8.8.8:10085", "127.0.0.1:28443"} {
		if _, err := applyStatsInfrastructure(agentFixtureRoot(t), server); err == nil {
			t.Fatalf("accepted unsafe or conflicting address %s", server)
		}
	}
	bad := agentFixtureRoot(t)
	bad["outbounds"] = []any{}
	if _, err := applyStatsInfrastructure(bad, "127.0.0.1:10085"); err == nil {
		t.Fatal("accepted empty outbounds")
	}
	bad = agentFixtureRoot(t)
	bad["inbounds"] = append(bad["inbounds"].([]any), map[string]any{"tag": "api-internal", "listen": "0.0.0.0", "port": json.Number("10085"), "protocol": "dokodemo-door"})
	if _, err := applyStatsInfrastructure(bad, "127.0.0.1:10085"); err == nil {
		t.Fatal("accepted public API listener")
	}
}

func TestStatsInspectionDetectsPublicListenerAndShadowedRoute(t *testing.T) {
	root := agentFixtureRoot(t)
	if _, err := applyStatsInfrastructure(root, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	api := root["inbounds"].([]any)[0].(map[string]any)
	api["listen"] = "0.0.0.0"
	routing := root["routing"].(map[string]any)
	routing["rules"] = append([]any{map[string]any{"type": "field", "network": "tcp", "outboundTag": "direct"}}, routing["rules"].([]any)...)
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := inspectStatsSwitches(writeRoutingConfig(t, string(data)), "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(report.APIMissing, ", ")
	if !strings.Contains(joined, "unsafe or mismatched") || !strings.Contains(joined, "api route") {
		t.Fatalf("unsafe API not reported: %+v", report)
	}
}

func TestRoutingValidationOrderingAndProtection(t *testing.T) {
	root := agentFixtureRoot(t)
	if _, err := applyStatsInfrastructure(root, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	rule, err := decodeRoutingRule([]byte(`{"ruleTag":"ads","domain":["domain:example.com"],"outboundTag":"block"}`))
	if err != nil {
		t.Fatal(err)
	}
	change := routingChange{Kind: "add", RuleTag: "ads", Rule: rule, Position: 1}
	if _, err := applyRoutingChange(root, change); err == nil {
		t.Fatal("inserted ahead of the protected API")
	}
	change.Position = -1
	if _, err := applyRoutingChange(root, change); err == nil {
		t.Fatal("accepted negative position")
	}
	change.Position = 2
	if _, err := applyRoutingChange(root, change); err != nil {
		t.Fatal(err)
	}
	if _, err := applyRoutingChange(root, change); err == nil {
		t.Fatal("accepted duplicate tag")
	}
	change.Kind = "update"
	change.Rule["outboundTag"] = "direct"
	if _, err := applyRoutingChange(root, change); err != nil {
		t.Fatal(err)
	}
	change.Rule["inboundTag"] = []any{"api-internal"}
	if _, err := applyRoutingChange(root, change); err == nil {
		t.Fatal("accepted protected inbound")
	}
	if _, err := applyRoutingChange(root, routingChange{Kind: "delete", RuleTag: "ads"}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"ruleTag":"bad","outboundTag":"direct"}`,
		`{"ruleTag":"bad","domain":["example.com"],"outboundTag":"missing","unknown":true}`,
		`{"ruleTag":"bad","type":[],"domain":["example.com"],"outboundTag":"direct"}`,
	} {
		if _, err := decodeRoutingRule([]byte(invalid)); err == nil {
			t.Fatalf("accepted rule %s", invalid)
		}
	}
}

func TestNativeOutboundAndDNSChanges(t *testing.T) {
	root := agentFixtureRoot(t)
	change := networkChange{Kind: "outbounds.put", Tag: "proxy", Value: map[string]any{"tag": "proxy", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "AsIs"}}}
	if _, err := applyNetworkChange(root, change); err != nil {
		t.Fatal(err)
	}
	if _, err := applyNetworkChange(root, networkChange{Kind: "outbounds.delete", Tag: "direct"}); err == nil {
		t.Fatal("deleted default outbound")
	}
	if _, err := applyNetworkChange(root, networkChange{Kind: "outbounds.delete", Tag: "api"}); err == nil {
		t.Fatal("deleted protected API outbound")
	}
	routing := root["routing"].(map[string]any)
	routing["rules"] = []any{map[string]any{"type": "field", "domain": []any{"domain:example.com"}, "outboundTag": "proxy"}}
	if _, err := applyNetworkChange(root, networkChange{Kind: "outbounds.delete", Tag: "proxy"}); err == nil {
		t.Fatal("deleted referenced outbound")
	}
	routing["rules"] = []any{}
	routing["balancers"] = []any{map[string]any{"tag": "b", "selector": []any{"pro"}}}
	if _, err := applyNetworkChange(root, networkChange{Kind: "outbounds.delete", Tag: "proxy"}); err == nil {
		t.Fatal("deleted outbound with balancers")
	}
	delete(routing, "balancers")
	if _, err := applyNetworkChange(root, networkChange{Kind: "outbounds.delete", Tag: "proxy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyNetworkChange(root, networkChange{Kind: "dns.put", Value: map[string]any{"servers": []any{"1.1.1.1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["dns"].(map[string]any); !ok {
		t.Fatal("DNS was not updated")
	}
}

func TestNetworkAgentPreviewOmitsCredentials(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	cfg.XrayBinPath = h.xray
	op, _ := findAgentOperation("outbounds.put")
	input := []byte(`{"outbound":{"tag":"proxy","protocol":"trojan","settings":{"password":"secret-value-123"}}}`)
	before, _ := os.ReadFile(h.config)
	result, err := executeAgentOperation(cfg, op, input, false, false)
	if err != nil {
		t.Fatal(err)
	}
	preview, _ := json.Marshal(result)
	if bytes.Contains(preview, []byte("secret-value-123")) {
		t.Fatalf("preview exposed secret: %s", preview)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 {
		t.Fatal("network preview changed config")
	}
}

func TestNetworkValidationErrorDoesNotEchoSecret(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	if err := os.WriteFile(h.xray, []byte("#!/bin/sh\necho secret-value-123 >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath = h.config
	cfg.XrayBinPath = h.xray
	before, _ := os.ReadFile(h.config)
	change := networkChange{Kind: "outbounds.put", Tag: "proxy", Value: map[string]any{"tag": "proxy", "protocol": "trojan", "settings": map[string]any{"password": "secret-value-123"}}}
	_, err := changeNetworkConfig(cfg, change)
	if err == nil || strings.Contains(err.Error(), "secret-value-123") {
		t.Fatalf("unsafe validation error: %v", err)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected config changed")
	}
}

func TestCoreRollbackHoldsConfigLockAndUsesConfiguredStatsAddress(t *testing.T) {
	h := newBindingMigrationHarness(t, agentFixture, false)
	statsLog := h.xray + ".stats-log"
	current := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + statsLog + "'\nprintf '%s\\n' '{\"stat\":[]}'\n"
	previous := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(h.xray, []byte(current), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.xray+".previous", []byte(previous), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireConfigLock(h.config, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := checkedCoreRollback(h.xray, h.config, "127.0.0.1:12345"); done <- err }()
	select {
	case err := <-done:
		releaseConfigLock(lock)
		t.Fatalf("rollback did not wait for lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	contents, err := os.ReadFile(h.xray)
	if err != nil || string(contents) != current {
		releaseConfigLock(lock)
		t.Fatalf("binary changed while config lock held: %v", err)
	}
	releaseConfigLock(lock)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rollback did not finish")
	}
	contents, err = os.ReadFile(h.xray)
	if err != nil || string(contents) != previous {
		t.Fatalf("previous binary not restored: %v", err)
	}
	log, err := os.ReadFile(statsLog)
	if err != nil || !bytes.Contains(log, []byte("--server=127.0.0.1:12345")) {
		t.Fatalf("wrong statistics address: %s %v", log, err)
	}
}

func TestPublicHostChangePreservesConfigAndTakesEffect(t *testing.T) {
	path := writeRoutingConfig(t, `{"token":"keep-secret","admin_ids":[123],"server_host":"old.example.com","custom":{"enabled":true}}`)
	cfg := &BotConfig{ConfigFile: path, ServerHost: "old.example.com"}
	if _, err := persistServerHost(path, "127.0.0.1"); err == nil {
		t.Fatal("accepted loopback public host")
	}
	if _, err := persistServerHost(path, "new.example.com"); err != nil {
		t.Fatal(err)
	}
	host, err := resolveServerHost(cfg)
	if err != nil || host != "new.example.com" {
		t.Fatalf("running Bot did not reload host: %q %v", host, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["token"] != "keep-secret" || saved["custom"] == nil {
		t.Fatal("unrelated config was lost")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe mode: %v", info.Mode())
	}
}

func TestConcurrentBotConfigUpdatesPreserveFieldsAndIntegers(t *testing.T) {
	path := writeRoutingConfig(t, `{"admin_ids":[9007199254740993],"token":"keep"}`)
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() { <-start; errs <- persistLanguage(path, "zh-CN") }()
	go func() { <-start; _, err := persistServerHost(path, "vpn.example.com"); errs <- err }()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"9007199254740993", `"server_host": "vpn.example.com"`, `"language": "zh-CN"`, `"token": "keep"`} {
		if !bytes.Contains(data, []byte(value)) {
			t.Fatalf("lost %q from %s", value, data)
		}
	}
}

func TestNetworkChangesAcceptedByRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	root := agentFixtureRoot(t)
	if _, err := applyNetworkChange(root, networkChange{Kind: "outbounds.put", Tag: "block2", Value: map[string]any{"tag": "block2", "protocol": "blackhole"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyNetworkChange(root, networkChange{Kind: "dns.put", Value: map[string]any{"servers": []any{"1.1.1.1"}}}); err != nil {
		t.Fatal(err)
	}
	rule, err := decodeRoutingRule([]byte(`{"ruleTag":"ads","domain":["domain:example.com"],"outboundTag":"block2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = applyRoutingChange(root, routingChange{Kind: "add", RuleTag: "ads", Rule: rule}); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	path := writeRoutingConfig(t, string(config))
	if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected network changes: %v\n%s", err, output)
	}
}

// Optional in CI; set XRAY_TEST_BIN to test the generated scaffold with a real core.
func TestStatsScaffoldWithRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	root := agentFixtureRoot(t)
	if _, err := applyStatsInfrastructure(root, "127.0.0.1:10085"); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	path := writeRoutingConfig(t, string(config))
	if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected scaffold: %v\n%s", err, output)
	}
}

func TestStatsScaffoldRunsWithRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_TEST_BIN")
	if bin == "" {
		t.Skip("XRAY_TEST_BIN not set")
	}
	freePort := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port
	}
	apiPort := freePort()
	root := agentFixtureRoot(t)
	root["inbounds"].([]any)[0].(map[string]any)["port"] = json.Number(strconv.Itoa(freePort()))
	address := fmt.Sprintf("127.0.0.1:%d", apiPort)
	if _, err := applyStatsInfrastructure(root, address); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	path := writeRoutingConfig(t, string(config))
	cmd := exec.Command(bin, "run", "-config", path)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, dialErr := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Xray API did not start: %v; %s", dialErr, output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if output, err := exec.Command(bin, "api", "statsquery", "-server", address).CombinedOutput(); err != nil {
		t.Fatalf("generated API cannot answer a statistics query: %v; %s", err, output)
	}
}
