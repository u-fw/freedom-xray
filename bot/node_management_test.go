package main

import (
 "encoding/json"
 "testing"
)

func TestNodeChangePathAndMode(t *testing.T) {
 var config map[string]any
 data:=[]byte(`{"inbounds":[{"tag":"api-internal","protocol":"dokodemo-door","settings":{}},{"tag":"cdn","protocol":"vless","streamSettings":{"network":"xhttp","security":"tls","xhttpSettings":{"path":"/old","mode":"auto","extra":{"xPaddingBytes":"100-1000"}}},"settings":{"clients":[{"id":"aaa","email":"admin"}]}}]}`)
 if err:=json.Unmarshal(data,&config);err!=nil{t.Fatal(err)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"path",Tag:"cdn",Value:"/stream-v2"});err!=nil{t.Fatal(err)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"mode",Tag:"cdn",Value:"packet-up"});err!=nil{t.Fatal(err)}
 inbound:=config["inbounds"].([]any)[1].(map[string]any)
 stream:=inbound["streamSettings"].(map[string]any)
 xhttp:=stream["xhttpSettings"].(map[string]any)
 if xhttp["path"]!="/stream-v2"||xhttp["mode"]!="packet-up"{t.Fatal("not applied",xhttp)}
 if xhttp["extra"]==nil{t.Fatal("XHTTP Extra lost")}
 for _,change:=range []NodeChange{{Kind:"mode",Tag:"cdn",Value:"bad"},{Kind:"path",Tag:"cdn",Value:"http://evil"},{Kind:"path",Tag:"cdn",Value:"/foo?x=1"},{Kind:"mode",Tag:"missing",Value:"auto"}}{
  if _,err:=applyManagedNodeChange(config,change);err==nil{t.Fatal("accepted unsafe change",change)}
 }
}

func TestNodeChangeListenerPort(t *testing.T) {
 var config map[string]any
 data:=[]byte(`{"inbounds":[{"tag":"a","port":443,"protocol":"vless","settings":{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}]},"streamSettings":{"network":"tcp","security":"reality"}},{"tag":"b","port":8443,"protocol":"vless","settings":{"clients":[{"id":"22222222-2222-2222-2222-222222222222","email":"b"}]},"streamSettings":{"network":"xhttp","security":"tls"}}]}`)
 if err:=json.Unmarshal(data,&config);err!=nil{t.Fatal(err)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"port",Tag:"a",Value:"9443"});err!=nil{t.Fatal(err)}
 inbound:=config["inbounds"].([]any)[0].(map[string]any)
 if inboundPortValue(inbound["port"])!=9443{t.Fatalf("port not applied: %#v",inbound["port"])}
 for _,change:=range []NodeChange{
  {Kind:"port",Tag:"a",Value:"8443"},   // used by another inbound
  {Kind:"port",Tag:"a",Value:"9443"},   // unchanged
  {Kind:"port",Tag:"a",Value:"0"},
  {Kind:"port",Tag:"a",Value:"65536"},
  {Kind:"port",Tag:"a",Value:"abc"},
  {Kind:"port",Tag:"missing",Value:"9444"},
 }{
  if _,err:=applyManagedNodeChange(config,change);err==nil{t.Fatalf("accepted invalid port change: %+v",change)}
 }
}

func TestNodeChangeXHTTPHostAndPadding(t *testing.T) {
 var config map[string]any
 data:=[]byte(`{"inbounds":[{"tag":"cdn","port":443,"protocol":"vless","settings":{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"cdn-user"}]},"streamSettings":{"network":"xhttp","security":"tls","xhttpSettings":{"path":"/s","mode":"auto","xPaddingBytes":"100-1000"}}}]}`)
 if err:=json.Unmarshal(data,&config);err!=nil{t.Fatal(err)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"xhttp_host",Tag:"cdn",Value:"cdn.example.com"});err!=nil{t.Fatal(err)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"xhttp_padding",Tag:"cdn",Value:"200-900"});err!=nil{t.Fatal(err)}
 xhttp:=config["inbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)["xhttpSettings"].(map[string]any)
 if xhttp["host"]!="cdn.example.com"{t.Fatalf("host not applied: %#v",xhttp)}
 extra,ok:=xhttp["extra"].(map[string]any)
 if !ok||extra["xPaddingBytes"]!="200-900"{t.Fatalf("padding not written to extra: %#v",xhttp)}
 if _,exists:=xhttp["xPaddingBytes"];exists{t.Fatalf("legacy top-level padding retained: %#v",xhttp)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"xhttp_host",Tag:"cdn",Value:""});err!=nil{t.Fatal(err)}
 if _,exists:=xhttp["host"];exists{t.Fatalf("host not cleared: %#v",xhttp)}
 for _,change:=range []NodeChange{
  {Kind:"xhttp_host",Tag:"cdn",Value:"https://cdn.example.com"},
  {Kind:"xhttp_host",Tag:"cdn",Value:"cdn.example.com:443"},
  {Kind:"xhttp_host",Tag:"cdn",Value:"bad host"},
  {Kind:"xhttp_padding",Tag:"cdn",Value:"900-200"},
  {Kind:"xhttp_padding",Tag:"cdn",Value:"0-70000"},
  {Kind:"xhttp_padding",Tag:"cdn",Value:"nope"},
  {Kind:"xhttp_padding",Tag:"cdn",Value:"200-900"}, // unchanged
 }{
  if _,err:=applyManagedNodeChange(config,change);err==nil{t.Fatalf("accepted invalid XHTTP change: %+v",change)}
 }
}

func TestNodeChangeRealityShortID(t *testing.T) {
 root:=realityTestConfig(t,"reality",[]string{"www.nvidia.com"},"www.nvidia.com:443","www.nvidia.com:443")
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"reality_short_id",Tag:"TCP-REALITY-443",Value:"0123456789abcdef"});err!=nil{t.Fatal(err)}
 settings:=realitySettingsFromRoot(t,root)
 ids,ok:=settings["shortIds"].([]any)
 if !ok||len(ids)!=1||ids[0]!="0123456789abcdef"{t.Fatalf("short ID not replaced: %#v",settings)}
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"reality_short_id",Tag:"TCP-REALITY-443",Value:""});err!=nil{t.Fatal(err)}
 ids=settings["shortIds"].([]any)
 if len(ids)!=1||ids[0]!=""{t.Fatalf("short ID not cleared: %#v",settings)}
 for _,value:=range []string{"xyz","abc","001122334455667788"}{
  if err:=validateRealityShortID(value);err==nil{t.Errorf("accepted invalid short ID %q",value)}
 }
 multi:=realityTestConfig(t,"reality",[]string{"www.nvidia.com"},"www.nvidia.com:443","www.nvidia.com:443")
 realitySettingsFromRoot(t,multi)["shortIds"]=[]any{"aa","bb"}
 if _,err:=applyManagedNodeChange(multi,NodeChange{Kind:"reality_short_id",Tag:"TCP-REALITY-443",Value:"cc"});err==nil{t.Fatal("allowed ambiguous multi-short-ID update")}
 tls:=realityTestConfig(t,"tls",[]string{"www.nvidia.com"},"www.nvidia.com:443","www.nvidia.com:443")
 if _,err:=applyManagedNodeChange(tls,NodeChange{Kind:"reality_short_id",Tag:"TCP-REALITY-443",Value:"cc"});err==nil{t.Fatal("allowed short-ID update on TLS inbound")}
}

func TestNodeChangeRealityDestinationAndKeys(t *testing.T) {
 root:=realityTestConfig(t,"reality",[]string{"www.nvidia.com"},"www.nvidia.com:443","www.nvidia.com:443")
 settings:=realitySettingsFromRoot(t,root)
 settings["publicKey"]="stale-public-key"
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"reality_dest",Tag:"TCP-REALITY-443",Value:"decoy.example.com:8443"});err!=nil{t.Fatal(err)}
 if settings["dest"]!="decoy.example.com:8443"{t.Fatalf("dest not applied: %#v",settings)}
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"reality_target",Tag:"TCP-REALITY-443",Value:"www.example.com:443"});err!=nil{t.Fatal(err)}
 if settings["target"]!="www.example.com:443"{t.Fatalf("target not applied: %#v",settings)}
 rotatedKey:="rotated-private-key-material-000000000000"
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"reality_private_key",Tag:"TCP-REALITY-443",Secret:rotatedKey});err!=nil{t.Fatal(err)}
 if settings["privateKey"]!=rotatedKey{t.Fatalf("private key not rotated: %#v",settings)}
 if _,exists:=settings["publicKey"];exists{t.Fatalf("stale public key retained: %#v",settings)}
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"reality_mldsa_seed",Tag:"TCP-REALITY-443",Secret:"seed-value"});err!=nil{t.Fatal(err)}
 if settings["mldsa65Seed"]!="seed-value"{t.Fatalf("ML-DSA seed not applied: %#v",settings)}
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"reality_mldsa_seed",Tag:"TCP-REALITY-443"});err!=nil{t.Fatal(err)}
 if _,exists:=settings["mldsa65Seed"];exists{t.Fatalf("ML-DSA seed not cleared: %#v",settings)}
 for _,change:=range []NodeChange{
  {Kind:"reality_dest",Tag:"TCP-REALITY-443",Value:"https://example.com"},
  {Kind:"reality_dest",Tag:"TCP-REALITY-443",Value:"example.com"},
  {Kind:"reality_target",Tag:"TCP-REALITY-443",Value:"example.com:0"},
  {Kind:"reality_target",Tag:"TCP-REALITY-443",Value:"bad host:443"},
  {Kind:"reality_private_key",Tag:"TCP-REALITY-443",Secret:""},
  {Kind:"reality_mldsa_seed",Tag:"TCP-REALITY-443"},
  {Kind:"reality_dest",Tag:"missing",Value:"example.com:443"},
 }{
  if _,err:=applyManagedNodeChange(root,change);err==nil{t.Fatalf("accepted invalid REALITY change: %+v",change)}
 }
 tls:=realityTestConfig(t,"tls",[]string{"www.nvidia.com"},"www.nvidia.com:443","www.nvidia.com:443")
 if _,err:=applyManagedNodeChange(tls,NodeChange{Kind:"reality_private_key",Tag:"TCP-REALITY-443",Secret:"key"});err==nil{t.Fatal("allowed REALITY key rotation on TLS")}
}

func TestNodeChangeDecryption(t *testing.T) {
 var config map[string]any
 data:=[]byte(`{"inbounds":[{"tag":"node","port":443,"protocol":"vless","settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"tcp","security":"reality"}}]}`)
 if err:=json.Unmarshal(data,&config);err!=nil{t.Fatal(err)}
 settings:=config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"decryption",Tag:"node",Secret:"new-decryption"});err!=nil{t.Fatal(err)}
 if settings["decryption"]!="new-decryption"{t.Fatalf("decryption not applied: %#v",settings)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"decryption",Tag:"node",Secret:"none"});err!=nil{t.Fatal(err)}
 if settings["decryption"]!="none"{t.Fatalf("decryption not disabled: %#v",settings)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"decryption",Tag:"node",Secret:"none"});err==nil{t.Fatal("accepted unchanged decryption")}
}

func TestNodeChangeTLSPairAndALPN(t *testing.T) {
 dir:=t.TempDir()
 certPath,keyPath:=writeTestCertificate(t,dir,"www.example.com")
 newDir:=t.TempDir()
 newCert,newKey:=writeTestCertificate(t,newDir,"www.example.com")
 otherCert,otherKey:=writeTestCertificate(t,newDir,"other.example.com")
 var config map[string]any
 raw:=map[string]any{"inbounds":[]any{map[string]any{
  "tag":"cdn","port":float64(443),"protocol":"vless",
  "settings":map[string]any{"clients":[]any{}},
  "streamSettings":map[string]any{
   "network":"xhttp","security":"tls",
   "tlsSettings":map[string]any{
    "serverName":"www.example.com",
    "certificates":[]any{map[string]any{"certificateFile":certPath,"keyFile":keyPath}},
    "alpn":[]any{"h2","http/1.1"},
   },
   "xhttpSettings":map[string]any{"path":"/s","mode":"auto"},
  },
 }}}
 serialized,_:=json.Marshal(raw)
 if err:=json.Unmarshal(serialized,&config);err!=nil{t.Fatal(err)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"tls_pair",Tag:"cdn",Value:newCert,Secret:newKey});err!=nil{t.Fatal(err)}
 tlsSettings:=config["inbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)
 cert:=tlsSettings["certificates"].([]any)[0].(map[string]any)
 if cert["certificateFile"]!=newCert||cert["keyFile"]!=newKey{t.Fatalf("pair not replaced: %#v",cert)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"tls_pair",Tag:"cdn",Value:otherCert,Secret:otherKey});err==nil{t.Fatal("accepted a certificate that does not cover the SNI")}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"tls_pair",Tag:"cdn",Value:certPath,Secret:newKey});err==nil{t.Fatal("accepted mismatched certificate and key")}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"tls_pair",Tag:"cdn",Value:"relative.crt",Secret:newKey});err==nil{t.Fatal("accepted relative certificate path")}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"alpn",Tag:"cdn",Value:"h2, http/1.1"});err!=nil{t.Fatal(err)}
 alpn:=tlsSettings["alpn"].([]any)
 if len(alpn)!=2||alpn[0]!="h2"||alpn[1]!="http/1.1"{t.Fatalf("ALPN not written: %#v",alpn)}
 if _,err:=applyManagedNodeChange(config,NodeChange{Kind:"alpn",Tag:"cdn",Value:"h3"});err==nil{t.Fatal("accepted unsupported ALPN")}
 tcp:=realityTestConfig(t,"reality",[]string{"www.nvidia.com"},"www.nvidia.com:443","www.nvidia.com:443")
 if _,err:=applyManagedNodeChange(tcp,NodeChange{Kind:"alpn",Tag:"TCP-REALITY-443",Value:"h2"});err==nil{t.Fatal("allowed ALPN on REALITY")}
}

func TestNodeChangeListenAddress(t *testing.T) {
 root:=realityTestConfig(t,"reality",[]string{"www.nvidia.com"},"www.nvidia.com:443","www.nvidia.com:443")
 inbound:=root["inbounds"].([]any)[0].(map[string]any)
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"listen",Tag:"TCP-REALITY-443",Value:"127.0.0.1"});err!=nil{t.Fatal(err)}
 if inbound["listen"]!="127.0.0.1"{t.Fatalf("listen address not applied: %#v",inbound)}
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"listen",Tag:"TCP-REALITY-443",Value:"not-an-ip"});err==nil{t.Fatal("accepted invalid listen address")}
 if _,err:=applyManagedNodeChange(root,NodeChange{Kind:"listen",Tag:"TCP-REALITY-443",Value:""});err!=nil{t.Fatal(err)}
 if _,exists:=inbound["listen"];exists{t.Fatalf("listen address not cleared: %#v",inbound)}
}

func TestParseInboundTraffic(t *testing.T){
 payload:=[]byte(`{"stat":[{"name":"inbound>>>cdn>>>traffic>>>uplink","value":"123"},{"name":"inbound>>>cdn>>>traffic>>>downlink","value":50},{"name":"inbound>>>direct>>>traffic>>>uplink","value":"9"},{"name":"user>>>admin>>>traffic>>>uplink","value":"20"}]}`)
 values,err:=parseInboundTraffic(payload);if err!=nil{t.Fatal(err)}
 if len(values)!=2{t.Fatal("unexpected node count",values)}
 if values[0].Tag!="cdn"||values[0].Total()!=173{t.Fatal("bad CDN totals",values[0])}
 if values[1].Tag!="direct"||values[1].Total()!=9{t.Fatal("bad direct totals",values[1])}
}
