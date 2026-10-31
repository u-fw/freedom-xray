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

func TestParseInboundTraffic(t *testing.T){
 payload:=[]byte(`{"stat":[{"name":"inbound>>>cdn>>>traffic>>>uplink","value":"123"},{"name":"inbound>>>cdn>>>traffic>>>downlink","value":50},{"name":"inbound>>>direct>>>traffic>>>uplink","value":"9"},{"name":"user>>>admin>>>traffic>>>uplink","value":"20"}]}`)
 values,err:=parseInboundTraffic(payload);if err!=nil{t.Fatal(err)}
 if len(values)!=2{t.Fatal("unexpected node count",values)}
 if values[0].Tag!="cdn"||values[0].Total()!=173{t.Fatal("bad CDN totals",values[0])}
 if values[1].Tag!="direct"||values[1].Total()!=9{t.Fatal("bad direct totals",values[1])}
}
