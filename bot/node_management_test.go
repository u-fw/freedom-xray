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

func TestParseInboundTraffic(t *testing.T){
 payload:=[]byte(`{"stat":[{"name":"inbound>>>cdn>>>traffic>>>uplink","value":"123"},{"name":"inbound>>>cdn>>>traffic>>>downlink","value":50},{"name":"inbound>>>direct>>>traffic>>>uplink","value":"9"},{"name":"user>>>admin>>>traffic>>>uplink","value":"20"}]}`)
 values,err:=parseInboundTraffic(payload);if err!=nil{t.Fatal(err)}
 if len(values)!=2{t.Fatal("unexpected node count",values)}
 if values[0].Tag!="cdn"||values[0].Total()!=173{t.Fatal("bad CDN totals",values[0])}
 if values[1].Tag!="direct"||values[1].Total()!=9{t.Fatal("bad direct totals",values[1])}
}
