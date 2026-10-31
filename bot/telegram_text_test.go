package main

import (
 "strings"
 "testing"
 "unicode/utf8"
)

func TestSplitTelegramTextKeepsShortMessagesWhole(t *testing.T){
 if chunks:=splitTelegramText("hello",telegramChunkLimit);len(chunks)!=1||chunks[0]!="hello"{
  t.Fatalf("short text was split: %+v",chunks)
 }
}

// A deployment with many users produces a traffic or online card far beyond
// Telegram's 4096-character limit. Splitting must keep every byte, respect the
// limit and never cut a UTF-8 rune.
func TestSplitTelegramTextPreservesContentAndBound(t *testing.T){
 var b strings.Builder
 for i:=0;i<500;i++{
  b.WriteString("👤 *user-name-")
  b.WriteString(strings.Repeat("x",10))
  b.WriteString("*\n  ⬆️ `1.00 MB` | ⬇️ `2.00 MB`\n\n")
 }
 text:=b.String()
 chunks:=splitTelegramText(text,telegramChunkLimit)
 if len(chunks)<2{t.Fatalf("long text was not split: %d chunk(s)",len(chunks))}
 for _,chunk:=range chunks{
  if len(chunk)>telegramChunkLimit{t.Fatalf("chunk exceeds the limit: %d",len(chunk))}
  if !utf8.ValidString(chunk){t.Fatal("chunk was split inside a rune")}
 }
 if strings.Join(chunks,"")!=text{t.Fatal("split chunks do not preserve the original text")}
}

func TestSplitTelegramTextHardSplitsOverlongLine(t *testing.T){
 // Cyrillic is two bytes per rune, so a hard split can land inside a rune.
 text:=strings.Repeat("а",telegramChunkLimit+97)
 chunks:=splitTelegramText(text,telegramChunkLimit)
 for _,chunk:=range chunks{
  if len(chunk)>telegramChunkLimit{t.Fatalf("chunk exceeds the limit: %d",len(chunk))}
  if !utf8.ValidString(chunk){t.Fatal("chunk was split inside a rune")}
 }
 if strings.Join(chunks,"")!=text{t.Fatal("hard split lost content")}
}
