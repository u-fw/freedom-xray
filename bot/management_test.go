package main

import (
 "encoding/json"
 "os"
 "path/filepath"
 "strings"
 "testing"
)

func testConfig(t *testing.T) map[string]any {
 t.Helper()
 const input = "{\"inbounds\":[{\"tag\":\"api-internal\",\"protocol\":\"dokodemo-door\",\"settings\":{}},{\"tag\":\"raw\",\"protocol\":\"vless\",\"streamSettings\":{\"network\":\"tcp\",\"security\":\"reality\"},\"settings\":{\"decryption\":\"none\",\"clients\":[{\"id\":\"aaa\",\"email\":\"admin\",\"flow\":\"xtls-rprx-vision\"}]}},{\"tag\":\"cdn\",\"protocol\":\"vless\",\"settings\":{\"decryption\":\"none\",\"clients\":[{\"id\":\"aaa\",\"email\":\"admin\"}]}}]}"
 var root map[string]any
 if err:=json.Unmarshal([]byte(input),&root);err!=nil {t.Fatal(err)}
 return root
}

func TestUserChangeSamplesConfiguredStatsServer(t *testing.T){
 h:=newBindingMigrationHarness(t,bindingMigrationConfig(),false)
 logPath:=filepath.Join(filepath.Dir(h.config),"xray-args")
 script:="#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+logPath+"'\ncase \"$*\" in\n *statsonlineiplist*) printf '%s\\n' '{\"users\":[]}' ;;\n *statsquery*) printf '%s\\n' '{\"stat\":[]}' ;;\nesac\nexit 0\n"
 if err:=os.WriteFile(h.xray,[]byte(script),0700);err!=nil{t.Fatal(err)}
 if _,err:=changeManagedUser(h.config,h.xray,"127.0.0.1:14000",UserChange{Kind:"add",User:"phone",Node:"raw"});err!=nil{t.Fatal(err)}
 args,err:=os.ReadFile(logPath);if err!=nil{t.Fatal(err)}
 if !strings.Contains(string(args),"statsquery --server=127.0.0.1:14000"){
  t.Fatalf("restart snapshot did not use configured Stats API: %s",args)
 }
}
func clientsIn(t *testing.T,root map[string]any,tag string) []any {
 t.Helper()
 inbounds,err:=usableInbounds(root);if err!=nil {t.Fatal(err)}
 for _,in:=range inbounds{if in["tag"]==tag{list,_:=clientList(in);return list}}
 t.Fatal("missing tag ",tag)
 return nil
}
func TestManagedUserCreationAndFlow(t *testing.T) {
 root:=testConfig(t)
 msg,err:=applyManagedUserChange(root,UserChange{Kind:"add",User:"phone",Node:"all"});if err!=nil {t.Fatal(err)}
 if !strings.Contains(msg,"UUID:"){t.Fatal(msg)}
 for _,tag:=range []string{"raw","cdn"} {
  list:=clientsIn(t,root,tag)
  if len(list)!=2 {t.Fatalf("%s clients %d",tag,len(list))}
  c,_:=getMap(list[1]);if c["email"]!=bindingEmail("phone",tag) {t.Fatal(c)}
  if tag=="raw" && c["flow"]!="xtls-rprx-vision" {t.Fatal("Vision not copied")}
  if tag=="cdn" && c["flow"]!=nil {t.Fatal("Unexpected Vision on CDN")}
 }
}
func TestManagedUserAssignRenameRotate(t *testing.T) {
 root:=testConfig(t)
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"add",User:"phone",Node:"raw"});err!=nil{t.Fatal(err)}
 if len(clientsIn(t,root,"cdn"))!=1{t.Fatal("added to wrong node")}
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"assign",User:"phone",Node:"cdn"});err!=nil{t.Fatal(err)}
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"rename",User:"phone",NewName:"mobile"});err!=nil{t.Fatal(err)}
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"rotate",User:"mobile"});err!=nil{t.Fatal(err)}
 in,err:=usableInbounds(root);if err!=nil{t.Fatal(err)}
 ids,err:=managedUserIDs(in,"mobile");if err!=nil || len(ids)!=1 {t.Fatalf("ids=%v err=%v",ids,err)}
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"unassign",User:"mobile",Node:"cdn"});err!=nil{t.Fatal(err)}
 if len(clientsIn(t,root,"cdn"))!=1{t.Fatal("unassign failed")}
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"delete",User:"mobile"});err!=nil{t.Fatal(err)}
 if len(clientsIn(t,root,"raw"))!=1{t.Fatal("delete failed")}
}
func TestManagedUserRestrictions(t *testing.T) {
 root:=testConfig(t)
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"add",User:"bad>>>name"});err==nil {t.Fatal("unsafe name accepted")}
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"delete",User:"admin"});err!=nil {t.Fatal("deleting last node client should be allowed",err)}
 root=testConfig(t)
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"add",User:"user",Node:"missing"});err==nil {t.Fatal("unknown node accepted")}
 inbounds,_:=usableInbounds(root)
 clients,_:=clientList(inbounds[1])
 user,_:=getMap(clients[0]);user["id"]="different"
 if _,err:=applyManagedUserChange(root,UserChange{Kind:"rotate",User:"admin"});err==nil {t.Fatal("conflicting UUID accepted")}
}
