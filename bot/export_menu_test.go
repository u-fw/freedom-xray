package main

import (
 "net/url"
 "strings"
 "testing"
)

func TestUserExportMenuBoundNodesAndBatch(t *testing.T){
 a:=InboundDetails{
  Protocol:"vless",Tag:"TCP-REALITY-443",Port:443,Network:"tcp",Security:"reality",
  SNI:"www.example.com",PrivateKey:"PRIVEXPORT",ShortID:"abcd",
  MLDSA65Seed:"SEEDEXPORT",ClientEncrypt:"mlkem768x25519plus.native.LONG",
  Clients:[]XrayClient{{ID:"11111111-1111-1111-1111-111111111111",Email:"admin",Flow:"xtls-rprx-vision"}},
 }
 b:=InboundDetails{
  Protocol:"vless",Tag:"XHTTP-TLS-CDN-55555",Port:55555,Network:"xhttp",Security:"tls",
  SNI:"cdn.example.com",XHTTPPath:"/stream",ClientEncrypt:"none",
  Clients:[]XrayClient{{ID:"11111111-1111-1111-1111-111111111111",Email:"admin"}},
 }
 c:=InboundDetails{
  Protocol:"vless",Tag:"other",Port:10000,Network:"tcp",Security:"reality",
  Clients:[]XrayClient{{ID:"22222222-2222-2222-2222-222222222222",Email:"phone"}},
 }
 derivationMu.Lock()
 derivationCache["x25519|PRIVEXPORT"]="PUBLICKEY"
 derivationCache["mldsa65|SEEDEXPORT"]="VERIFYKEY"
 derivationMu.Unlock()
 entries:=boundUserExportEntries([]InboundDetails{b,c,a},"admin")
 if len(entries)!=2{t.Fatalf("expected 2 bound nodes, got %d",len(entries))}
 if entries[0].Node.Port!=443||entries[1].Node.Port!=55555{t.Fatalf("unexpected sort or nodes: %+v",entries)}
 text,issues:=assembleBatchUserLinks(entries,"203.0.113.7")
 if len(issues)!=0{t.Fatalf("batch errors: %v",issues)}
 lines:=strings.Split(strings.TrimSpace(text),"\n")
 if len(lines)!=2{t.Fatalf("want 2 complete links, got %d",len(lines))}
 reality,err:=url.Parse(lines[0]);if err!=nil{t.Fatal(err)}
 q:=reality.Query()
 for k,want:=range map[string]string{
  "pbk":"PUBLICKEY","pqv":"VERIFYKEY","sid":"abcd",
  "type":"tcp","security":"reality",
  "flow":"xtls-rprx-vision","encryption":"mlkem768x25519plus.native.LONG",
 }{
  if q.Get(k)!=want{t.Fatalf("REALITY %s = %q, expected %q",k,q.Get(k),want)}
 }
 if reality.Port()!="443"{t.Fatal("reality missing 443 port")}
 cdn,err:=url.Parse(lines[1]);if err!=nil{t.Fatal(err)}
 if cdn.Port()!="55555"||cdn.Query().Get("type")!="xhttp"||cdn.Query().Get("path")!="/stream"{
  t.Fatal("CDN link lost parameters")
 }
 phone:=boundUserExportEntries([]InboundDetails{a,b,c},"phone")
 if len(phone)!=1||phone[0].Node.Tag!="other"{t.Fatal("different user got admin's links")}
}

func TestUserExportReportsInvalidRealityAndContinues(t *testing.T){
 broken:=userExportEntry{
  Node:InboundDetails{Protocol:"vless",Tag:"reality-broken",Port:443,Network:"tcp",Security:"reality"},
  Client:XrayClient{ID:"11111111-1111-1111-1111-111111111111",Email:"user"},
 }
 good:=userExportEntry{
  Node:InboundDetails{Protocol:"vless",Tag:"cdn",Port:55555,Network:"xhttp",Security:"tls",SNI:"cdn.example.org"},
  Client:XrayClient{ID:"11111111-1111-1111-1111-111111111111",Email:"user"},
 }
 txt,issues:=assembleBatchUserLinks([]userExportEntry{broken,good},"203.0.113.5")
 if len(issues)!=1||!strings.Contains(issues[0],"pbk"){t.Fatalf("missing explicit REALITY error: %v",issues)}
 if strings.Count(txt,"vless://")!=1||!strings.Contains(txt,":55555?"){t.Fatalf("valid node not exported: %s",txt)}
}
