package main

import (
 "encoding/json"
 "strings"
 "testing"
)

func decodeStatsConfig(t *testing.T,body string) map[string]any {
 t.Helper()
 var root map[string]any
 if err:=json.Unmarshal([]byte(body),&root);err!=nil{t.Fatal(err)}
 return root
}

// The policy helper must enable all three layers without disturbing unrelated
// settings, and must be idempotent so a repair never restarts Xray needlessly.
func TestEnableStatsPolicy(t *testing.T) {
 root:=decodeStatsConfig(t,`{"policy":{"levels":{"0":{"handshake":4},"3":{"statsUserOnline":true}}},"inbounds":[{"settings":{"clients":[{"level":3},{"level":"7"}]}}]}`)
 if !enableStatsPolicy(root){t.Fatal("missing statistics flags were not detected")}
 policy,_:=getMap(root["policy"])
 system,_:=getMap(policy["system"])
 if system["statsInboundUplink"]!=true||system["statsInboundDownlink"]!=true{
  t.Fatalf("inbound statistics not enabled: %#v",system)
 }
 levels,_:=getMap(policy["levels"])
 for _,name:=range []string{"0","3","7"} {
  level,_:=getMap(levels[name])
  for _,flag:=range []string{"statsUserUplink","statsUserDownlink","statsUserOnline"} {
   if level[flag]!=true{t.Fatalf("level %s %s not enabled: %#v",name,flag,level)}
  }
 }
 level0,_:=getMap(levels["0"])
 if level0["handshake"]!=float64(4){t.Fatalf("unrelated policy setting was lost: %#v",level0)}
 if enableStatsPolicy(root){t.Fatal("an already complete policy was reported as changed")}
}

const statsSwitchFixture=`{
 "stats":{},
 "api":{"tag":"api","services":["StatsService"]},
 "policy":{"system":{"statsInboundUplink":false},"levels":{
  "0":{"statsUserOnline":true,"statsUserUplink":true,"statsUserDownlink":true},
  "2":{"statsUserOnline":false,"statsUserUplink":true,"statsUserDownlink":false}
 }},
 "inbounds":[
  {"tag":"api-internal","settings":{"clients":[]}},
  {"tag":"reality","settings":{"clients":[{"level":0},{"level":2}]},"streamSettings":{"network":"tcp"}},
  {"tag":"cdn","settings":{"clients":[]},"streamSettings":{"network":"xhttp","sockopt":{}}},
  {"tag":"cdn-ok","settings":{"clients":[]},"streamSettings":{"network":"xhttp","sockopt":{"trustedXForwardedFor":["CF-Connecting-IP"]}}}
 ],
 "outbounds":[{"tag":"api","protocol":"freedom"}],
 "routing":{"rules":[{"inboundTag":["api-internal"],"outboundTag":"api"}]}
}`

func TestInspectStatsSwitches(t *testing.T) {
 report,err:=inspectStatsSwitches(writeDiagnosticsConfig(t,statsSwitchFixture))
 if err!=nil{t.Fatal(err)}
 if report.SystemInbound{t.Fatal("disabled inbound statistics reported as enabled")}
 if len(report.MissingOnlineLevels)!=1||report.MissingOnlineLevels[0]!="2"{
  t.Fatalf("unexpected missing online levels: %#v",report.MissingOnlineLevels)
 }
 if len(report.MissingTrafficLevels)!=1||report.MissingTrafficLevels[0]!="2"{
  t.Fatalf("unexpected missing traffic levels: %#v",report.MissingTrafficLevels)
 }
 if len(report.APIMissing)!=0{t.Fatalf("complete API scaffold reported as missing: %#v",report.APIMissing)}
 if len(report.XHTTPWithoutXFF)!=1||report.XHTTPWithoutXFF[0]!="cdn"{
  t.Fatalf("unexpected XHTTP XFF report: %#v",report.XHTTPWithoutXFF)
 }
 if report.policyComplete(){t.Fatal("incomplete policy reported as complete")}
}

func TestInspectStatsSwitchesReportsAPIGaps(t *testing.T) {
 report,err:=inspectStatsSwitches(writeDiagnosticsConfig(t,`{"inbounds":[]}`))
 if err!=nil{t.Fatal(err)}
 joined:=strings.Join(report.APIMissing,", ")
 for _,wanted:=range []string{"stats","StatsService","api-internal","api outbound","api route"} {
  if !strings.Contains(joined,wanted){t.Fatalf("missing %q in API gaps: %s",wanted,joined)}
 }
}

func TestStatsSwitchesTextExplainsOffSwitches(t *testing.T) {
 report,_:=inspectStatsSwitches(writeDiagnosticsConfig(t,statsSwitchFixture))
 text:=statsSwitchesText("en",report,nil)
 for _,wanted:=range []string{"Per-user traffic","level(s) 2","Online IPs","Inbound/node traffic","Stats API","cdn"} {
  if !strings.Contains(text,wanted){t.Fatalf("missing %q in stats card: %s",wanted,text)}
 }
 if strings.Contains(text,"stats."){t.Fatalf("raw locale key leaked into the card: %s",text)}
 if !strings.Contains(statsSwitchesText("en",statsSwitchReport{SystemInbound:true},nil),"All statistics switches are enabled"){
  t.Fatal("complete policy must say all switches are enabled")
 }
}
