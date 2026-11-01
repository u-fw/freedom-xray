package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/u-fw/freedom-xray/bot/internal/xrayapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type routeRPCService interface {
	TestRoute(context.Context, *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error)
}

type routeRPCHandler func(context.Context, *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error)

func (handler routeRPCHandler) TestRoute(ctx context.Context, request *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error) {
	return handler(ctx, request)
}

func newRouteRPCFixture(t *testing.T, handler routeRPCHandler) (*BotConfig, bindingMigrationHarness) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "xray.app.router.command.RoutingService", HandlerType: (*routeRPCService)(nil),
		Methods: []grpc.MethodDesc{{MethodName: "TestRoute", Handler: func(service any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			request := new(xrayapi.TestRouteRequest)
			if err := decode(request); err != nil {
				return nil, err
			}
			call := func(ctx context.Context, raw any) (any, error) {
				return service.(routeRPCService).TestRoute(ctx, raw.(*xrayapi.TestRouteRequest))
			}
			if interceptor != nil {
				return interceptor(ctx, request, &grpc.UnaryServerInfo{Server: service, FullMethod: routeTestMethod}, call)
			}
			return call(ctx, request)
		}}},
	}, handler)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-done; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("routing fixture server failed: %v", err)
		}
	})
	root := agentFixtureRoot(t)
	root["api"] = map[string]any{"tag": "api", "listen": listener.Addr().String(), "services": []any{"RoutingService"}}
	data, _ := json.Marshal(root)
	h := newBindingMigrationHarness(t, string(data), false)
	cfg := DefaultConfig()
	cfg.XrayConfigPath, cfg.XrayBinPath, cfg.StatsAPIServer, cfg.AllowMutations = h.config, h.xray, listener.Addr().String(), false
	return cfg, h
}

func TestRouteQuerySendsFullContextWithoutPublishingOrExposingExtraResponseFields(t *testing.T) {
	requests := make(chan *xrayapi.TestRouteRequest, 1)
	cfg, h := newRouteRPCFixture(t, func(_ context.Context, request *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error) {
		requests <- request
		return &xrayapi.RoutingContext{OutboundTag: "block", OutboundGroupTags: []string{"pool"}, User: "private-response-secret", Attributes: map[string]string{"token": "private-response-secret"}}, nil
	})
	before, _ := os.ReadFile(h.config)
	input := routeTestInput{Domain: "Example.COM.", IP: "2001:db8::2", Port: 53, Network: "UDP", InboundTag: "raw", Protocol: "quic", User: "alice",
		SourceIPs: []string{"::ffff:192.0.2.1"}, SourcePort: 12345, LocalIPs: []string{"127.0.0.1"}, LocalPort: 4443, VlessRoute: 123, Attributes: map[string]string{"x-test": "value"}}
	raw, _ := json.Marshal(input)
	op, _ := findAgentOperation("routing.test")
	result, err := executeAgentOperation(cfg, op, raw, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if !bytes.Contains(encoded, []byte(`"outbound_tag":"block"`)) || bytes.Contains(encoded, []byte("private-response-secret")) {
		t.Fatalf("unexpected route result: %s", encoded)
	}
	request := <-requests
	context := request.RoutingContext
	if request.PublishResult || strings.Join(request.FieldSelectors, ",") != "outbound" || context.TargetDomain != "Example.COM." || context.Network != xrayapi.Network_NETWORK_UDP || context.TargetPort != 53 || context.InboundTag != "raw" || context.Protocol != "quic" || context.User != "alice" || context.SourcePort != 12345 || context.LocalPort != 4443 || context.VlessRoute != 123 || context.Attributes["x-test"] != "value" || len(context.SourceIps[0]) != 4 || len(context.TargetIps[0]) != 16 || len(context.LocalIps[0]) != 4 {
		t.Fatalf("route context was lost or published: %v", request)
	}
	after, _ := os.ReadFile(h.config)
	if !bytes.Equal(before, after) || h.restartCount(t) != 0 || op.Mutation {
		t.Fatal("routing read changed configuration or restarted the core")
	}
}

func TestRouteQueryKeepsRPCErrorsPrivateAndOnlyRecognizesExactNoMatch(t *testing.T) {
	for _, test := range []struct {
		Code    codes.Code
		Message string
		NoMatch bool
	}{
		{codes.Unknown, "common: not enough information for making a decision", true},
		{codes.Unknown, "not enough information for making a decision", true},
		{codes.Unknown, "private-rpc-secret: not enough information for making a decision", false},
		{codes.InvalidArgument, "not enough information for making a decision", false},
		{codes.Unavailable, "private-rpc-secret", false},
		{codes.Unimplemented, "private-rpc-secret", false},
		{codes.DeadlineExceeded, "private-rpc-secret", false},
		{codes.Internal, "private-rpc-secret", false},
	} {
		cfg, _ := newRouteRPCFixture(t, func(context.Context, *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error) {
			return nil, grpcstatus.Error(test.Code, test.Message)
		})
		result, err := queryRouteTest(cfg, routeTestInput{Domain: "example.com"})
		if test.NoMatch {
			if err != nil || result.Matched || result.SavedDefaultOutbound == nil || result.SavedDefaultOutbound.Tag != "direct" {
				t.Fatalf("no-match result was lost: %+v %v", result, err)
			}
		} else if err == nil || strings.Contains(err.Error(), "private-rpc-secret") || result.SavedDefaultOutbound != nil {
			t.Fatalf("RPC failure leaked or became a default-outbound success: %+v %v", result, err)
		}
	}
}

func TestRouteQueryPinsLoopbackAndRejectsOversizedResponses(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { proxyCalls.Add(1); w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(proxy.Close)
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	cfg, h := newRouteRPCFixture(t, func(context.Context, *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error) {
		return &xrayapi.RoutingContext{OutboundTag: strings.Repeat("x", 300*1024)}, nil
	})
	if _, err := queryRouteTest(cfg, routeTestInput{Domain: "example.com"}); err == nil || proxyCalls.Load() != 0 {
		t.Fatal("routing query accepted an oversized response or used an environment proxy")
	}
	root, _ := decodeBindingMigrationConfig(h.config)
	root["api"].(map[string]any)["listen"] = "0.0.0.0:10085"
	data, _ := json.Marshal(root)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := queryRouteTest(cfg, routeTestInput{Domain: "example.com"}); err == nil {
		t.Fatal("query accepted a public API listener")
	}
}

func TestRouteRPCDeadlineIsBounded(t *testing.T) {
	cfg, _ := newRouteRPCFixture(t, func(ctx context.Context, _ *xrayapi.TestRouteRequest) (*xrayapi.RoutingContext, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	request, _, _, _ := buildRouteTestContext(routeTestInput{IP: "192.0.2.1"})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := routeTestRPC(ctx, cfg.StatsAPIServer, request); grpcstatus.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("RPC did not honor its deadline: %v", err)
	}
}

func TestRouteTestInputAcceptsTargetsAndRejectsUnsafeOrAmbiguousContexts(t *testing.T) {
	for _, text := range []string{"example.com", "example.com:8443", "udp 1.1.1.1:53", "[2001:db8::1]:443", "2001:db8::1", `{"domain":"example.com","ip":"192.0.2.1","source_ips":["127.0.0.1"]}`} {
		if _, err := parseRouteTestInput([]byte(text), false); err != nil {
			t.Fatalf("valid target rejected: %s: %v", text, err)
		}
	}
	for _, text := range []string{"", "https://example.com/private", "example.com:0", "example.com:65536", "udp example.com:bad", "tcp host extra", `{"ip":"192.0.2.1","target_ips":["192.0.2.2"]}`, `{"domain":"example.com","publish_result":true}`, `{"domain":"127.0.0.1"}`, `{"ip":"fe80::1%eth0"}`, `{"ip":"192.0.2.0/24"}`, `{"ip":"192.0.2.1","network":"unix"}`, `{"ip":"192.0.2.1","source_port":65536}`} {
		if _, err := parseRouteTestInput([]byte(text), false); err == nil {
			t.Fatalf("invalid context accepted: %s", text)
		}
	}
	if _, err := parseRouteTestInput([]byte("example.com"), true); err == nil {
		t.Fatal("JSON document accepted plain target text")
	}
	if _, _, _, err := buildRouteTestContext(routeTestInput{Domain: "\u4f8b\u5b50.\u6d4b\u8bd5"}); err != nil {
		t.Fatalf("IDN target was rejected: %v", err)
	}
}
