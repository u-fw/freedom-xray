package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAllTrafficResetRequiresMutationPermission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","message_id":1}}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	marker := filepath.Join(dir, "called")
	bin := filepath.Join(dir, "xray")
	script := "#!/bin/sh\nprintf called > '" + marker + "'\nprintf '%s\\n' '{\"stat\":[]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveTrafficStore(config, trafficStore{Users: map[string]trafficCounter{"alice": {TotalUp: 123}}}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.XrayConfigPath = config
	cfg.XrayBinPath = bin
	service := &BotService{cfg: cfg, tg: newTestTelegramClient(t, testBotToken, server.URL, server.Client())}
	service.askTrafficReset(123, 0)
	service.doTrafficReset(123, 0) // Covers stale confirmation buttons too.
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Xray reset ran with mutations disabled")
	}
	state, err := loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if state.Users["alice"].TotalUp != 123 {
		t.Fatal("traffic changed with mutations disabled")
	}
	cfg.AllowMutations = true
	service.doTrafficReset(123, 0)
	if _, err = os.Stat(marker); err != nil {
		t.Fatalf("authorized reset did not reach Xray: %v", err)
	}
	state, err = loadTrafficStore(config)
	if err != nil {
		t.Fatal(err)
	}
	if state.Users["alice"].TotalUp != 0 {
		t.Fatal("authorized reset did not clear the ledger")
	}
}

func TestTelegramSOCKSDialHonorsCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn) // Deliberately never answer the SOCKS handshake.
	}()
	client, err := telegramHTTPClient(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("missing proxy transport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	conn, err := transport.DialContext(ctx, "tcp", "example.com:443")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("stalled proxy unexpectedly connected")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("proxy dial did not honor the context deadline")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy connection remained open")
	}
}
