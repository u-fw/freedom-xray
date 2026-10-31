package main

import (
 "encoding/json"
 "os"
 "path/filepath"
 "testing"
)

func TestVersionProvisioningRetriesCollectorRestartFailure(t *testing.T){
 dir:=t.TempDir()
 config:=filepath.Join(dir,"config.json")
 root:=testConfig(t)
 enableStatsPolicy(root)
 raw,err:=json.Marshal(root);if err!=nil{t.Fatal(err)}
 if err=os.WriteFile(config,raw,0600);err!=nil{t.Fatal(err)}
 failureFlag:=filepath.Join(dir,"fail-restart")
 if err=os.WriteFile(failureFlag,[]byte("1"),0600);err!=nil{t.Fatal(err)}
 script:="#!/bin/sh\nif [ \"$1\" = restart ] && [ -f '"+failureFlag+"' ]; then exit 1; fi\nexit 0\n"
 if err=os.WriteFile(filepath.Join(dir,"systemctl"),[]byte(script),0700);err!=nil{t.Fatal(err)}
 t.Setenv("PATH",dir+string(os.PathListSeparator)+os.Getenv("PATH"))
 cfg:=DefaultConfig()
 cfg.XrayConfigPath=config
 marker:=filepath.Join(dir,".freedom-bot-version")
 applyVersionProvisioning(cfg)
 if _,err=os.Stat(marker);!os.IsNotExist(err){t.Fatal("failed restart was marked complete")}
 if err=os.Remove(failureFlag);err!=nil{t.Fatal(err)}
 applyVersionProvisioning(cfg)
 data,err:=os.ReadFile(marker);if err!=nil{t.Fatal(err)}
 if string(data)!=Version+"\n"{t.Fatalf("wrong provisioned version: %q",data)}
}

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

// A card warning is only useful for levels that actually own clients; enabled
// levels and levels without clients must not be reported as missing.
func TestMissingOnlineLevels(t *testing.T) {
 file:=writeDiagnosticsConfig(t,`{"policy":{"levels":{"0":{"statsUserOnline":false},"1":{"statsUserOnline":true},"5":{}}},"inbounds":[{"tag":"reality","settings":{"clients":[{"email":"c"},{"email":"a","level":1},{"email":"b","level":5}]}},{"tag":"api-internal","settings":{"clients":[{"level":7}]}}]}`)
 missing,err:=missingOnlineLevels(file)
 if err!=nil{t.Fatal(err)}
 if len(missing)!=2||missing[0]!="0"||missing[1]!="5"{
  t.Fatalf("unexpected missing levels: %#v",missing)
 }
 if _,err=missingOnlineLevels(writeDiagnosticsConfig(t,`{"policy":`));err==nil{
  t.Fatal("malformed config reported as complete policy")
 }
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
