package main

import (
 "encoding/json"
 "os"
 "path/filepath"
 "strings"
 "testing"
)

func testPublicPortConfig(t *testing.T)string{
 t.Helper()
 path:=filepath.Join(t.TempDir(),"config.json")
 raw:=[]byte(`{"inbounds":[
 {"tag":"cdn","port":55555,"protocol":"vless","settings":{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"admin"}]},"streamSettings":{"network":"xhttp","security":"tls","xhttpSettings":{"path":"/s","mode":"auto"},"tlsSettings":{"serverName":"cdn.example.org"}}},
 {"tag":"reality","port":443,"protocol":"vless","settings":{"clients":[]},"streamSettings":{"network":"tcp","security":"reality"}}]}`)
 if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
 return path
}

func TestPublicPortOverrideExportOnly(t *testing.T){
 path:=testPublicPortConfig(t)
 before,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
 message,err:=SetInboundPublicPort(path,"cdn",443)
 if err!=nil{t.Fatal(err)}
 if !strings.Contains(message,"443")||!strings.Contains(message,"55555"){t.Fatal(message)}
 after,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
 if string(before)!=string(after){t.Fatal("config.json changed unexpectedly")}
 nodes,err:=LoadXrayInbounds(path);if err!=nil{t.Fatal(err)}
 var cdn InboundDetails
 for _,node:=range nodes{if node.Tag=="cdn"{cdn=node}}
 if cdn.Port!=55555||cdn.SharePort()!=443{t.Fatalf("wrong listener or public port: %+v",cdn)}
 uri:=cdn.GenerateVlessURI(cdn.Clients[0],"203.0.113.7")
 if !strings.Contains(uri,"cdn.example.org:443?"){t.Fatal("missing public port in URI",uri)}
 if strings.Contains(uri,":55555?"){t.Fatal("exposed origin port",uri)}
 if !strings.Contains(uri,"mode=auto"){t.Fatal("missing top-level xhttp mode")}
 if !strings.Contains(uri,"xPaddingBytes"){t.Fatal("extra padding missing")}
 var saved publicPortConfig
 bytes,err:=os.ReadFile(publicPortsPath(path));if err!=nil{t.Fatal(err)}
 if err=json.Unmarshal(bytes,&saved);err!=nil{t.Fatal(err)}
 if saved.Nodes["cdn"].ListenPort!=55555||saved.Nodes["cdn"].PublicPort!=443{t.Fatal(saved)}
 st,err:=os.Stat(publicPortsPath(path));if err!=nil{t.Fatal(err)}
 if st.Mode().Perm()!=0600{t.Fatalf("world readable sidecar: %o",st.Mode().Perm())}
 if _,err=SetInboundPublicPort(path,"cdn",0);err!=nil{t.Fatal(err)}
 nodes,err=LoadXrayInbounds(path);if err!=nil{t.Fatal(err)}
 for _,node:=range nodes{if node.Tag=="cdn"&&node.SharePort()!=55555{t.Fatal("override not cleared")}}
}

func TestPublicPortValidationAndStaleness(t *testing.T){
 path:=testPublicPortConfig(t)
 for _,tc:=range []struct{tag string;port int}{{"reality",8443},{"missing",443},{"cdn",-1},{"cdn",65536}} {
  if _,err:=SetInboundPublicPort(path,tc.tag,tc.port);err==nil{t.Fatalf("invalid override allowed: %+v",tc)}
 }
 if _,err:=SetInboundPublicPort(path,"cdn",443);err!=nil{t.Fatal(err)}
 nodes,err:=LoadXrayInbounds(path);if err!=nil{t.Fatal(err)}
 for i:=range nodes{if nodes[i].Tag=="cdn"{nodes[i].Port=55556}}
 cfg,err:=readPublicPorts(path);if err!=nil{t.Fatal(err)}
 applyPublicPortOverrides(nodes,cfg)
 for _,n:=range nodes{if n.Tag=="cdn"&&n.SharePort()!=55556{t.Fatal("stale listener mapping incorrectly applied")}}
}
