package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Long traffic lists are paged so the buttons stay under the rows.
func TestTrafficCardPagesLongUserLists(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	var clients []string
	users := map[string]trafficCounter{}
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("user%02d", i)
		clients = append(clients, fmt.Sprintf(`{"email":%q,"id":"99999999-9999-4999-8999-9999999999%02d"}`, name, i))
		users[name] = trafficCounter{TotalUp: uint64(1000 - i)}
	}
	raw := `{"inbounds":[{"tag":"reality","protocol":"vless","settings":{"clients":[` + strings.Join(clients, ",") + `]}}]}`
	if err := os.WriteFile(config, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveTrafficStore(config, trafficStore{Users: users}); err != nil {
		t.Fatal(err)
	}
	cfg := &BotConfig{Language: "en", LanguageExplicit: true, AdminIDs: []int64{17}, XrayConfigPath: config, XrayBinPath: filepath.Join(t.TempDir(), "missing-xray")}
	s, capture := newCapturedRoutingBot(t, cfg)

	s.sendTrafficPage(9801, 3, 0)
	text, keyboard := capture.card(t)
	callbacks := strings.Join(keyboardCallbacks(t, keyboard), " ")
	if !strings.Contains(text, "Page 1 of 2") || !strings.Contains(text, "user00") || !strings.Contains(text, "user14") || strings.Contains(text, "user15") {
		t.Fatalf("first page = %q", text)
	}
	if !strings.Contains(callbacks, "traffic_page:15") || strings.Contains(callbacks, "traffic_page:-") {
		t.Fatalf("first page buttons = %s", callbacks)
	}

	s.sendTrafficPage(9801, 3, 15)
	text, keyboard = capture.card(t)
	callbacks = strings.Join(keyboardCallbacks(t, keyboard), " ")
	if !strings.Contains(text, "Page 2 of 2") || !strings.Contains(text, "user19") || strings.Contains(text, "user14") {
		t.Fatalf("second page = %q", text)
	}
	if !strings.Contains(callbacks, "traffic_page:0") || !strings.Contains(callbacks, "menu_inbound_traffic") {
		t.Fatalf("second page buttons = %s", callbacks)
	}
	if !strings.Contains(callbacks, "traffic_page:15") {
		t.Fatalf("refresh does not stay on the current page: %s", callbacks)
	}
}
