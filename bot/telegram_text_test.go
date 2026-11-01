package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTelegramHTMLKeepsShortMessagesWhole(t *testing.T) {
	if chunks := splitTelegramHTML("hello", telegramChunkLimit); len(chunks) != 1 || chunks[0] != "hello" {
		t.Fatalf("short text was split: %+v", chunks)
	}
}

// A deployment with many users produces a traffic or online card far beyond
// Telegram's 4096-character limit. Splitting must keep every visible
// character, respect the limit and never cut a UTF-8 rune.
func TestSplitTelegramHTMLPreservesContentAndBound(t *testing.T) {
	var b htmlBuilder
	for i := 0; i < 500; i++ {
		b.Add("👤 ", bold("user-name-"+strings.Repeat("x", 10)), "\n  ⬆️ ", code("1.00 MB"), " | ⬇️ ", code("2.00 MB"), "\n\n")
	}
	text := b.HTML()
	chunks := splitTelegramHTML(text, telegramChunkLimit)
	if len(chunks) < 2 {
		t.Fatalf("long text was not split: %d chunk(s)", len(chunks))
	}
	var joined strings.Builder
	for _, chunk := range chunks {
		if len(chunk) > telegramChunkLimit {
			t.Fatalf("chunk exceeds the limit: %d", len(chunk))
		}
		if !utf8.ValidString(string(chunk)) {
			t.Fatal("chunk was split inside a rune")
		}
		joined.WriteString(string(chunk))
	}
	if joined.String() != string(text) {
		t.Fatal("line-boundary chunks do not preserve the original text")
	}
}

func TestSplitTelegramHTMLHardSplitsOverlongLine(t *testing.T) {
	// Cyrillic is two bytes per rune, so a hard split can land inside a rune.
	text := esc(strings.Repeat("а", telegramChunkLimit+97))
	chunks := splitTelegramHTML(text, telegramChunkLimit)
	if len(chunks) < 2 {
		t.Fatalf("overlong line was not split: %d chunk(s)", len(chunks))
	}
	var joined strings.Builder
	for _, chunk := range chunks {
		if len(chunk) > telegramChunkLimit {
			t.Fatalf("chunk exceeds the limit: %d", len(chunk))
		}
		if !utf8.ValidString(string(chunk)) {
			t.Fatal("chunk was split inside a rune")
		}
		joined.WriteString(string(chunk))
	}
	if joined.String() != string(text) {
		t.Fatal("hard split lost content")
	}
}
