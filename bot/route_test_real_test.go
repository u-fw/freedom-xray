package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	grpcstatus "google.golang.org/grpc/status"
)

func startRouteTestCore(t *testing.T, mode string) *BotConfig {
	t.Helper()
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
	root := agentFixtureRoot(t)
	root["inbounds"].([]any)[0].(map[string]any)["port"] = json.Number(fmt.Sprint(freePort()))
	apiAddress := fmt.Sprintf("127.0.0.1:%d", freePort())
	if mode == "direct" {
		root["api"] = map[string]any{"tag": "api", "listen": apiAddress}
	}
	root["routing"].(map[string]any)["rules"] = []any{
		map[string]any{"type": "field", "domain": []any{"regexp:^casesensitive[.]example[.]$"}, "outboundTag": "block"},
		map[string]any{"type": "field", "domain": []any{"domain:route.example"}, "outboundTag": "block"},
		map[string]any{"type": "field", "ip": []any{"198.51.100.0/24"}, "outboundTag": "block"},
		map[string]any{"type": "field", "inboundTag": []any{"raw"}, "user": []any{"synthetic-user"}, "protocol": []any{"tls"}, "outboundTag": "block"},
		map[string]any{"type": "field", "source": []any{"192.0.2.0/24"}, "sourcePort": "12345", "localIP": []any{"127.0.0.1/32"}, "localPort": "4443", "network": "udp", "port": "53", "vlessRoute": "123", "attrs": map[string]any{"x-test": "^value$"}, "outboundTag": "block"},
	}
	if _, err := applyRoutingService(root, apiAddress); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(root)
	path := writeRoutingConfig(t, string(data))
	logPath := filepath.Join(filepath.Dir(path), "route-core.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bin, "run", "-config", path)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); _ = logFile.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", apiAddress, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			output, _ := os.ReadFile(logPath)
			t.Fatalf("API did not start: %s", output)
		}
		time.Sleep(25 * time.Millisecond)
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, cfg.AllowMutations = path, bin, apiAddress, false
	return cfg
}

func TestRouteQueryAgainstRealXray(t *testing.T) {
	for _, mode := range []string{"direct", "inbound"} {
		t.Run(mode, func(t *testing.T) {
			cfg := startRouteTestCore(t, mode)
			before, _ := os.ReadFile(cfg.XrayConfigPath)
			for _, input := range []routeTestInput{
				{Domain: "CaseSensitive.example."},
				{Domain: "route.example"},
				{IP: "198.51.100.2", Port: 8443},
				{Domain: "other.invalid", InboundTag: "raw", User: "synthetic-user", Protocol: "tls"},
				{IP: "203.0.113.2", Network: "udp", Port: 53, SourceIPs: []string{"192.0.2.1"}, SourcePort: 12345, LocalIPs: []string{"127.0.0.1"}, LocalPort: 4443, VlessRoute: 123, Attributes: map[string]string{"x-test": "value"}},
			} {
				result, err := queryRouteTest(cfg, input)
				if err != nil || !result.Matched || result.OutboundTag != "block" || len(result.ConfigETag) != 24 || result.CheckedAt.IsZero() {
					t.Fatalf("real route query failed for %+v: %+v %v", input, result, err)
				}
			}
			result, err := queryRouteTest(cfg, routeTestInput{Domain: "unmatched.invalid"})
			if err != nil || result.Matched || result.OutboundTag != "" || result.SavedDefaultOutbound == nil || result.SavedDefaultOutbound.Tag != "direct" {
				request, _, _, _ := buildRouteTestContext(routeTestInput{Domain: "unmatched.invalid"})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, rpcErr := routeTestRPC(ctx, cfg.StatsAPIServer, request)
				t.Fatalf("no-match/default hint was misreported: %+v %v; RPC code=%s message=%q", result, err, grpcstatus.Code(rpcErr), grpcstatus.Convert(rpcErr).Message())
			}
			withoutDot, err := queryRouteTest(cfg, routeTestInput{Domain: "CaseSensitive.example"})
			if err != nil || withoutDot.Matched {
				t.Fatalf("regex-sensitive trailing dot was rewritten: %+v %v", withoutDot, err)
			}
			after, _ := os.ReadFile(cfg.XrayConfigPath)
			if !bytes.Equal(before, after) {
				t.Fatal("route query changed the configuration")
			}
		})
	}
}
