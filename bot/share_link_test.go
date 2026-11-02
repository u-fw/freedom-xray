package main

import (
	"strings"
	"testing"
)

// Links are shown in a tap-to-copy code span; a copy button is added when the
// link fits Telegram's copy limit. Both must reproduce the URI exactly.
func TestShareLinksAreCopyableAndExact(t *testing.T) {
	s, capture, chat, _ := inputSessionBot(t)
	item := userExportEntry{Node: InboundDetails{Tag: "raw<1>", Port: 443, Network: "tcp", Security: "reality"}}
	short := "vless://id@203.0.113.7:443?security=reality&sni=www.example.com&fp=chrome#raw"
	if !s.sendUserLink(chat.ID, item, "alice", short) {
		t.Fatal("short link was not sent")
	}
	text, keyboard := capture.card(t)
	if !strings.Contains(text, "<code>"+string(esc(short))+"</code>") || !strings.Contains(text, "raw&lt;1&gt;") {
		t.Fatalf("link or tag not rendered safely: %q", text)
	}
	if len(keyboard.InlineKeyboard) != 1 || keyboard.InlineKeyboard[0][0].Copy != short {
		t.Fatalf("short link lacks an exact copy button: %+v", keyboard)
	}
	long := "vless://id@203.0.113.7:443?encryption=mlkem768x25519plus.native.0rtt." + strings.Repeat("A", 300) + "&security=reality#raw"
	if !s.sendUserLink(chat.ID, item, "alice", long) {
		t.Fatal("long link was not sent")
	}
	capture.mu.Lock()
	text, markup := capture.text, capture.markup
	capture.mu.Unlock()
	if !strings.Contains(text, string(esc(long))) || markup != "" {
		t.Fatalf("long link was altered or offered an over-limit copy button: markup=%q", markup)
	}
	if plainHTML(htmlText(text)) == "" || !strings.HasSuffix(plainHTML(htmlText(text)), long) {
		t.Fatal("visible text does not end with the exact link")
	}
}
