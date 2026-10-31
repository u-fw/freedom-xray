package main

import (
 "encoding/json"
 "testing"
)

func TestIndependentUserDirectoryAndBinding(t *testing.T){
 root:=testConfig(t)
 dir,err:=directoryFromConfig(root);if err!=nil{t.Fatal(err)}
 if _,ok:=dir.Users["admin"];!ok{t.Fatal("legacy admin migration missing")}
 msg,err:=applyDirectoryUserChange(root,&dir,UserChange{Kind:"add",User:"phone"},true)
 if err!=nil{t.Fatal(err)}
 if msg==""||dir.Users["phone"].ID==""{t.Fatal("independent user not stored")}
 if len(clientsIn(t,root,"raw"))!=1||len(clientsIn(t,root,"cdn"))!=1{t.Fatal("unbound account unexpectedly copied")}
 if _,err=applyDirectoryUserChange(root,&dir,UserChange{Kind:"assign",User:"phone",Node:"raw"},true);err!=nil{t.Fatal(err)}
 if len(clientsIn(t,root,"raw"))!=2||len(clientsIn(t,root,"cdn"))!=1{t.Fatal("binding leaked across inbounds")}
 if _,err=applyDirectoryUserChange(root,&dir,UserChange{Kind:"unassign",User:"phone",Node:"raw"},true);err!=nil{t.Fatal(err)}
 if _,ok:=dir.Users["phone"];!ok{t.Fatal("unassign deleted user identity")}
 if len(clientsIn(t,root,"raw"))!=1{t.Fatal("unassign ineffective")}
 if _,err=applyDirectoryUserChange(root,&dir,UserChange{Kind:"delete",User:"admin"},true);err!=nil{t.Fatal(err)}
 if len(clientsIn(t,root,"raw"))!=0||len(clientsIn(t,root,"cdn"))!=0{t.Fatal("last inbound client cannot be removed")}
}

func TestVisionCompatibilityPolicy(t *testing.T){
 cases:=[]struct{network,security,decryption string; want bool}{
  {"tcp","reality","none",true},
  {"tcp","tls","none",true},
  {"xhttp","reality","none",false},
  {"xhttp","tls","none",false},
  {"xhttp","reality","mlkem768x25519plus.native",true},
  {"xhttp","tls","mlkem768x25519plus.native",true},
 }
 for _,tc:=range cases {
  in:=map[string]any{"streamSettings":map[string]any{"network":tc.network,"security":tc.security},"settings":map[string]any{"clients":[]any{},"decryption":tc.decryption}}
  got:=userFlowForInbound(in)!=""
  if got!=tc.want{t.Errorf("%s/%s/%s flow=%v want=%v",tc.network,tc.security,tc.decryption,got,tc.want)}
 }
}

func TestDirectoryPersistsWithoutActiveInbound(t *testing.T){
 root:=testConfig(t)
 for _,in:=range root["inbounds"].([]any){
  m:=in.(map[string]any)
  if m["protocol"]=="vless"{m["settings"].(map[string]any)["clients"]=[]any{}}
 }
 directory:=userDirectory{Users:map[string]directoryUser{"phone":{ID:"11111111-1111-1111-1111-111111111111"}}}
 if _,err:=applyDirectoryUserChange(root,&directory,UserChange{Kind:"assign",User:"phone",Node:"cdn"},true);err!=nil{t.Fatal(err)}
 if len(clientsIn(t,root,"cdn"))!=1||len(clientsIn(t,root,"raw"))!=0{t.Fatal("incorrect mapping")}
 data,err:=json.Marshal(directory);if err!=nil{t.Fatal(err)}
 var recovered userDirectory;if err:=json.Unmarshal(data,&recovered);err!=nil{t.Fatal(err)}
 if recovered.Users["phone"].ID!=directory.Users["phone"].ID{t.Fatal("directory lost UUID")}
}
