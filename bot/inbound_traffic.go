package main

import (
 "context"
 "encoding/json"
 "fmt"
 "os/exec"
 "sort"
 "strconv"
 "strings"
)

type InboundTraffic struct { Tag string; Uplink uint64; Downlink uint64 }
func (t InboundTraffic) Total() uint64 {return t.Uplink+t.Downlink}

// Xray StatsService exposes per-inbound totals; it does not expose
// per-user-per-inbound breakdowns without separate accounting.
func QueryInboundTraffic(ctx context.Context,xrayBin,server string)([]InboundTraffic,error){
 output,err:=exec.CommandContext(ctx,xrayBin,"api","statsquery","--server="+server,"-pattern=inbound>>>").Output()
 if err!=nil{return nil,fmt.Errorf("Xray 入站统计查询失败: %w",err)}
 return parseInboundTraffic(output)
}
func parseInboundTraffic(data []byte)([]InboundTraffic,error){
 var root struct{Stat []struct{Name string `json:"name"`;Value any `json:"value"`} `json:"stat"`}
 if err:=json.Unmarshal(data,&root);err!=nil{return nil,err}
 traffic:=map[string]*InboundTraffic{}
 for _,item:=range root.Stat{
  parts:=strings.Split(item.Name,">>>")
  if len(parts)!=4||parts[0]!="inbound"||parts[2]!="traffic"{continue}
  value:=uint64(0)
  switch v:=item.Value.(type) {
  case string: n,e:=strconv.ParseUint(v,10,64);if e!=nil{continue};value=n
  case float64: if v<0{continue};value=uint64(v)
  default:continue
  }
  t:=traffic[parts[1]];if t==nil{t=&InboundTraffic{Tag:parts[1]};traffic[parts[1]]=t}
  switch parts[3]{case "uplink":t.Uplink+=value;case "downlink":t.Downlink+=value}
 }
 out:=make([]InboundTraffic,0,len(traffic));for _,v:=range traffic{out=append(out,*v)}
 sort.Slice(out,func(i,j int)bool{return out[i].Tag<out[j].Tag})
 return out,nil
}
