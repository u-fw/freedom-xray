package main

import (
 "encoding/json"
 "os"
 "path/filepath"
 "strings"
 "testing"
)

func bindingMigrationConfig() string {
 return `{
  "inbounds": [
   {"tag":"api-internal","protocol":"dokodemo-door","settings":{}},
   {"tag":"raw","protocol":"vless","settings":{"decryption":"none","clients":[{"id":"11111111-1111-4111-8111-111111111111","email":"alice","flow":"xtls-rprx-vision"}]}},
   {"tag":"cdn","protocol":"vless","settings":{"decryption":"none","clients":[{"id":"11111111-1111-4111-8111-111111111111","email":"alice"}]}}
  ],
  "outbounds": []
 }`
}

func TestApplyBindingMigrationBindAndRevert(t *testing.T){
 root:=testConfig(t)
 store:=suspendedStore{Users:map[string]suspendedAccount{"phone":{
  UUID:"22222222-2222-4222-8222-222222222222",
  Nodes:map[string]map[string]any{"raw":{"id":"22222222-2222-4222-8222-222222222222","email":"phone"}},
 }}}
 stats,err:=applyBindingEmailMigration(root,&store,bindingMigrate)
 if err!=nil{t.Fatal(err)}
 if stats.Rewritten!=2||stats.Inbounds!=2{t.Fatalf("unexpected first migration: %+v",stats)}
 if stats.SuspendedRewritten!=1{t.Fatalf("suspension store not rewritten: %+v",stats)}
 for _,tag:=range []string{"raw","cdn"}{
  c,_:=getMap(clientsIn(t,root,tag)[0])
  if c["email"]!=bindingEmail("admin",tag){t.Fatalf("%s admin binding: %+v",tag,c)}
 }
 if store.Users["phone"].Nodes["raw"]["email"]!=bindingEmail("phone","raw"){t.Fatalf("suspended binding: %+v",store.Users["phone"])}
 again,err:=applyBindingEmailMigration(root,&store,bindingMigrate)
 if err!=nil{t.Fatal(err)}
 if again.Rewritten!=0||again.AlreadyBound!=2{t.Fatalf("migration not idempotent: %+v",again)}
 reverted,err:=applyBindingEmailMigration(root,&store,bindingRevert)
 if err!=nil{t.Fatal(err)}
 if reverted.Rewritten!=2||reverted.SuspendedRewritten!=1{t.Fatalf("revert counts: %+v",reverted)}
 for _,tag:=range []string{"raw","cdn"}{
  c,_:=getMap(clientsIn(t,root,tag)[0])
  if c["email"]!="admin"{t.Fatalf("%s revert failed: %+v",tag,c)}
 }
 if store.Users["phone"].Nodes["raw"]["email"]!="phone"{t.Fatalf("suspended revert failed: %+v",store.Users["phone"])}
}

func TestApplyBindingMigrationRejectsConflicts(t *testing.T){
 cases:=[]struct{
  name string
  mutate func(map[string]any)
 }{
  {"duplicate tag",func(root map[string]any){
   in:=root["inbounds"].([]any)[2].(map[string]any);in["tag"]="raw"
  }},
  {"duplicate client email",func(root map[string]any){
   in:=root["inbounds"].([]any)[1].(map[string]any)
   list:=in["settings"].(map[string]any)["clients"].([]any)
   in["settings"].(map[string]any)["clients"]=append(list,map[string]any{"id":"33333333-3333-4333-8333-333333333333","email":"admin"})
  }},
  {"uuid mismatch",func(root map[string]any){
   in:=root["inbounds"].([]any)[2].(map[string]any)
   list:=in["settings"].(map[string]any)["clients"].([]any)
   list[0].(map[string]any)["id"]="44444444-4444-4444-8444-444444444444"
  }},
  {"binding on wrong inbound",func(root map[string]any){
   in:=root["inbounds"].([]any)[2].(map[string]any)
   list:=in["settings"].(map[string]any)["clients"].([]any)
   list[0].(map[string]any)["email"]=bindingEmail("admin","raw")
  }},
  {"invalid tag",func(root map[string]any){
   in:=root["inbounds"].([]any)[1].(map[string]any);in["tag"]="bad>>>tag"
  }},
 }
 for _,test:=range cases{
  t.Run(test.name,func(t *testing.T){
   root:=testConfig(t)
   store:=suspendedStore{Users:map[string]suspendedAccount{}}
   test.mutate(root)
   if _,err:=applyBindingEmailMigration(root,&store,bindingMigrate);err==nil{t.Fatal("conflict was accepted")}
  })
 }
}

func TestNormalizeSuspendedBindingsPreservesUnknownRecords(t *testing.T){
 store:=suspendedStore{Users:map[string]suspendedAccount{"alice":{
  UUID:"x",
  Nodes:map[string]map[string]any{
   "raw":{"email":"custom@example.com"},
   "bad>>>tag":{"email":"alice"},
  },
 }}}
 result:=normalizeSuspendedBindings(&store,bindingMigrate)
 if result.rewritten!=0||len(result.skipped)!=2{t.Fatalf("unknown records were guessed: %+v",result)}
 if store.Users["alice"].Nodes["raw"]["email"]!="custom@example.com"{t.Fatal("custom suspension email was rewritten")}
}

type bindingMigrationHarness struct{
 config string
 xray string
 systemctl string
 restarts string
}

func newBindingMigrationHarness(t *testing.T, config string, failValidation bool) bindingMigrationHarness {
 t.Helper()
 dir:=t.TempDir()
 h:=bindingMigrationHarness{
  config:filepath.Join(dir,"config.json"),
  xray:filepath.Join(dir,"xray"),
  systemctl:filepath.Join(dir,"systemctl"),
  restarts:filepath.Join(dir,"restarts.log"),
 }
 if err:=os.WriteFile(h.config,[]byte(config),0600);err!=nil{t.Fatal(err)}
 validate:="exit 0"
 if failValidation{validate="echo 'bad config' >&2; exit 1"}
 xrayScript:="#!/bin/sh\ncase \"$*\" in\n *run*-test*) "+validate+" ;;\n *statsonlineiplist*) printf '%s\\n' '{\"users\":[]}' ;;\n *statsquery*|*stats*) printf '%s\\n' '{\"stat\":[]}' ;;\n *) exit 0 ;;\nesac\n"
 if err:=os.WriteFile(h.xray,[]byte(xrayScript),0700);err!=nil{t.Fatal(err)}
 systemctlScript:="#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+h.restarts+"'\nexit 0\n"
 if err:=os.WriteFile(h.systemctl,[]byte(systemctlScript),0700);err!=nil{t.Fatal(err)}
 t.Setenv("PATH",dir+string(os.PathListSeparator)+os.Getenv("PATH"))
 return h
}

func (h bindingMigrationHarness) configEmails(t *testing.T) map[string]string {
 t.Helper()
 data,err:=os.ReadFile(h.config);if err!=nil{t.Fatal(err)}
 var root map[string]any
 if err=json.Unmarshal(data,&root);err!=nil{t.Fatal(err)}
 out:=map[string]string{}
 for _,value:=range root["inbounds"].([]any){
  in,_:=getMap(value);tag,_:=in["tag"].(string)
  settings,_:=getMap(in["settings"]);list,_:=settings["clients"].([]any)
  for _,entry:=range list{
   c,_:=getMap(entry);email,_:=c["email"].(string)
   out[tag]=email
  }
 }
 return out
}

func (h bindingMigrationHarness) restartCount(t *testing.T) int {
 t.Helper()
 data,err:=os.ReadFile(h.restarts)
 if os.IsNotExist(err){return 0}
 if err!=nil{t.Fatal(err)}
 count:=0
 for _,line:=range strings.Split(string(data),"\n"){
  if strings.TrimSpace(line)=="restart xray"{count++}
 }
 return count
}

func TestBindingMigrationTransactionMarkerBackupAndRevert(t *testing.T){
 h:=newBindingMigrationHarness(t,bindingMigrationConfig(),false)
 suspended:=`{"users":{"phone":{"uuid":"99999999-9999-4999-8999-999999999999","nodes":{"raw":{"id":"99999999-9999-4999-8999-999999999999","email":"phone"}}}}}`
 if err:=os.WriteFile(suspendedStorePath(h.config),[]byte(suspended),0600);err!=nil{t.Fatal(err)}

 stats,err:=migrateBindingEmails(h.config,h.xray,"127.0.0.1:10085")
 if err!=nil{t.Fatal(err)}
 if stats.Rewritten!=2||stats.SuspendedRewritten!=1{t.Fatalf("migration counts: %+v",stats)}
 emails:=h.configEmails(t)
 if emails["raw"]!=bindingEmail("alice","raw")||emails["cdn"]!=bindingEmail("alice","cdn"){t.Fatalf("config not migrated: %+v",emails)}
 data,err:=os.ReadFile(suspendedStorePath(h.config));if err!=nil{t.Fatal(err)}
 if !strings.Contains(string(data),bindingEmail("phone","raw")){t.Fatalf("suspension store not migrated: %s",data)}
 marker,err:=trafficMetaValue(h.config,bindingEmailsMarker);if err!=nil{t.Fatal(err)}
 if marker!="1"{t.Fatalf("marker=%q",marker)}
 if stats.BackupPath==""{t.Fatal("accounting backup was not recorded")}
 if _,err=os.Stat(stats.BackupPath);err!=nil{t.Fatalf("backup missing: %v",err)}
 restarts:=h.restartCount(t)
 if restarts!=1{t.Fatalf("migration restarts=%d",restarts)}
 again,err:=migrateBindingEmails(h.config,h.xray,"127.0.0.1:10085")
 if err!=nil{t.Fatal(err)}
 if again.changed(){t.Fatalf("second migration repeated work: %+v",again)}
 if h.restartCount(t)!=restarts{t.Fatal("no-op migration restarted Xray")}
 reverted,err:=revertBindingEmails(h.config,h.xray,"127.0.0.1:10085")
 if err!=nil{t.Fatal(err)}
 if reverted.Rewritten!=2||reverted.SuspendedRewritten!=1{t.Fatalf("revert counts: %+v",reverted)}
 emails=h.configEmails(t)
 if emails["raw"]!="alice"||emails["cdn"]!="alice"{t.Fatalf("config not reverted: %+v",emails)}
 marker,err=trafficMetaValue(h.config,bindingEmailsMarker);if err!=nil{t.Fatal(err)}
 if marker!="0"{t.Fatalf("revert marker=%q",marker)}
 restarts=h.restartCount(t)
 if skips,err:=migrateBindingEmails(h.config,h.xray,"127.0.0.1:10085");err!=nil||skips.changed(){t.Fatalf("reverted install re-migrated: %+v %v",skips,err)}
 if h.restartCount(t)!=restarts{t.Fatal("disabled migration restarted Xray")}
 forced,err:=migrateBindingEmailsForced(h.config,h.xray,"127.0.0.1:10085")
 if err!=nil{t.Fatal(err)}
 if !forced.changed(){t.Fatalf("forced CLI migration was skipped: %+v",forced)}
 emails=h.configEmails(t)
 if emails["raw"]!=bindingEmail("alice","raw")||emails["cdn"]!=bindingEmail("alice","cdn"){t.Fatalf("forced migration did not apply: %+v",emails)}
 marker,err=trafficMetaValue(h.config,bindingEmailsMarker);if err!=nil{t.Fatal(err)}
 if marker!="1"{t.Fatalf("forced marker=%q",marker)}
}

func TestBindingMigrationValidationFailureLeavesFilesUntouched(t *testing.T){
 original:=bindingMigrationConfig()
 h:=newBindingMigrationHarness(t,original,true)
 before,err:=os.ReadFile(h.config);if err!=nil{t.Fatal(err)}
 if _,err=migrateBindingEmails(h.config,h.xray,"127.0.0.1:10085");err==nil{t.Fatal("invalid staged config was committed")}
 after,err:=os.ReadFile(h.config);if err!=nil{t.Fatal(err)}
 if string(after)!=string(before){t.Fatalf("config changed despite validation failure:\n%s",after)}
 marker,err:=trafficMetaValue(h.config,bindingEmailsMarker);if err!=nil{t.Fatal(err)}
 if marker!=""{t.Fatalf("marker written after failure: %q",marker)}
}
