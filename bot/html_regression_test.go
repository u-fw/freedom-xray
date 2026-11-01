package main

import (
	"strings"
	"testing"
)

// Legacy Markdown turned "a_b_c" into italics and rejected a lone "_", which
// made confirmation cards show the wrong user name. HTML keeps names intact
// and escapes markup characters instead of interpreting them.
func TestUserConfirmationKeepsNamesIntactInHTML(t *testing.T) {
	cfg := &BotConfig{Language: "en", AllowMutations: true, AdminIDs: []int64{17}}
	s, capture := newCapturedRoutingBot(t, cfg)
	s.offerUserChange(9701, 17, 0, UserChange{Kind: "delete", User: "a_b_c"})
	capture.mu.Lock()
	text, mode := capture.text, capture.parseMode
	capture.mu.Unlock()
	if mode != "HTML" || !strings.Contains(text, "a_b_c") || strings.Contains(text, `\_`) {
		t.Fatalf("delete confirmation lost the user name or parse mode %q: %q", mode, text)
	}
	s.offerUserChange(9701, 17, 0, UserChange{Kind: "assign", User: "a_b_c", Node: "<b>x&y</b>"})
	capture.mu.Lock()
	text = capture.text
	capture.mu.Unlock()
	if !strings.Contains(text, "&lt;b&gt;x&amp;y&lt;/b&gt;") || strings.Contains(text, "<b>x&y</b>") {
		t.Fatalf("inbound tag was not escaped: %q", text)
	}
}
