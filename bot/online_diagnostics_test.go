package main

import (
 "os"
 "path/filepath"
 "testing"
)

func writeDiagnosticsConfig(t *testing.T,body string) string {
 t.Helper()
 file:=filepath.Join(t.TempDir(),"config.json")
 if err:=os.WriteFile(file,[]byte(body),0600);err!=nil{t.Fatal(err)}
 return file
}

func TestInspectOnlinePolicy(t *testing.T) {
 file:=writeDiagnosticsConfig(t,`{"policy":{"levels":{"0":{"statsUserOnline":true},"1":{"statsUserOnline":false},"2":{"statsUserOnline":true}}},"inbounds":[{"tag":"api-internal","settings":{"clients":[{"level":9}]}},{"tag":"reality","settings":{"clients":[{"email":"a","level":1},{"email":"b","level":2}]}},{"tag":"legacy","settings":{"clients":[{"email":"c"}]}}]}`)
 levels,err:=inspectOnlinePolicy(file)
 if err!=nil{t.Fatal(err)}
 if len(levels)!=3{t.Fatalf("unexpected levels: %#v",levels)}
 if levels[0].Level!="0"||!levels[0].Enabled||levels[0].Clients!=1{
  t.Fatalf("default level wrong (implicit level 0 must count): %#v",levels[0])
 }
 if levels[1].Level!="1"||levels[1].Enabled||levels[1].Clients!=1{t.Fatalf("level 1 wrong: %#v",levels[1])}
 if levels[2].Level!="2"||!levels[2].Enabled||levels[2].Clients!=1{t.Fatalf("level 2 wrong: %#v",levels[2])}
 for _,item:=range levels {
  if item.Level=="9"{t.Fatalf("internal API inbound must be ignored: %#v",levels)}
 }
}

func TestInspectOnlinePolicyRejectsMalformedConfig(t *testing.T) {
 if _,err:=inspectOnlinePolicy(writeDiagnosticsConfig(t,`{"policy":`));err==nil{t.Fatal("accepted malformed config")}
 if _,err:=inspectOnlinePolicy(filepath.Join(t.TempDir(),"missing.json"));err==nil{t.Fatal("accepted missing config")}
}

func TestOnlineAggregateEmpty(t *testing.T) {
 empty:=[]string{``,`{"users":[]}`,`{"users": null}`,`{"users":[] }`}
 for _,raw:=range empty {
  if !onlineAggregateEmpty(raw){t.Fatalf("empty aggregate not detected: %q",raw)}
 }
 for _,raw:=range []string{`{"users":[{"email":"a","ips":[]}]}`,`{"name":"a","ips":{"203.0.113.7":1}}`} {
  if onlineAggregateEmpty(raw){t.Fatalf("data misread as empty: %q",raw)}
 }
}
