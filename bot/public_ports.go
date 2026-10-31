package main

import (
 "encoding/json"
 "errors"
 "fmt"
 "log"
 "os"
 "path/filepath"
)

// Public ports describe an existing external HTTPS/Origin Rule route.
// They do not create a Cloudflare rule or modify Xray's listening port.
type publicPortEntry struct {
 ListenPort int `json:"listen_port"`
 PublicPort int `json:"public_port"`
}
type publicPortConfig struct{
 Nodes map[string]publicPortEntry `json:"nodes"`
}

func publicPortsPath(configPath string)string{
 return filepath.Join(filepath.Dir(configPath),"freedom-public-ports.json")
}
func readPublicPorts(configPath string)(publicPortConfig,error){
 v:=publicPortConfig{Nodes:map[string]publicPortEntry{}}
 raw,err:=os.ReadFile(publicPortsPath(configPath))
 if os.IsNotExist(err){return v,nil}
 if err!=nil{return v,err}
 if err=json.Unmarshal(raw,&v);err!=nil{return v,fmt.Errorf("对外端口配置 JSON 无效: %w",err)}
 if v.Nodes==nil{v.Nodes=map[string]publicPortEntry{}}
 for tag,e:=range v.Nodes{
  if tag==""||e.ListenPort<1||e.ListenPort>65535||e.PublicPort<1||e.PublicPort>65535{
   return v,fmt.Errorf("对外端口记录 %q 包含非法端口",tag)
  }
 }
 return v,nil
}

func applyPublicPortOverrides(nodes []InboundDetails,overrides publicPortConfig) {
 for i:=range nodes{
  n:=&nodes[i]
  entry,ok:=overrides.Nodes[n.Tag]
  if !ok{continue}
  if entry.ListenPort!=n.Port {
   log.Printf("[Bot] public-port override for %s ignored: listener changed from %d to %d",n.Tag,entry.ListenPort,n.Port)
   continue
  }
  if n.Network!="xhttp"||n.Security!="tls"{
   log.Printf("[Bot] public-port override for %s ignored: only XHTTP + TLS is supported",n.Tag)
   continue
  }
  n.PublicPort=entry.PublicPort
 }
}

func attachInboundPublicPorts(configPath string,nodes []InboundDetails)error{
 overrides,err:=readPublicPorts(configPath)
 if err!=nil{return err}
 applyPublicPortOverrides(nodes,overrides)
 return nil
}

// Public port is intentionally a sidecar mutation, not an Xray config edit:
// no Xray restart, no firewall modifications, no TLS changes.
func SetInboundPublicPort(configPath,tag string,publicPort int)(string,error){
 if tag==""||publicPort<0||publicPort>65535{return "",errors.New("非法对外端口，必须为 0（清除）或 1–65535")}
 lock,err:=acquireConfigLock(configPath,20_000_000_000);if err!=nil{return "",err}
 defer releaseConfigLock(lock)
 data,err:=os.ReadFile(configPath);if err!=nil{return "",err}
 var root map[string]any
 if err=json.Unmarshal(data,&root);err!=nil{return "",err}
 inbounds,err:=usableInbounds(root);if err!=nil{return "",err}
 found:=false;listenPort:=0
 for _,in:=range inbounds{
  if in["tag"]!=tag{continue}
  if found{return "",errors.New("节点 Tag 重复，拒绝覆盖")}
  stream,ok:=getMap(in["streamSettings"])
  if !ok||stream["network"]!="xhttp"||stream["security"]!="tls"{
   return "",errors.New("仅允许 XHTTP + TLS 节点设置 CDN 对外端口")
  }
  portFloat,ok:=in["port"].(float64)
  if !ok||portFloat<1||portFloat>65535{return "",errors.New("节点实际监听端口无效")}
  listenPort=int(portFloat)
  found=true
 }
 if !found{return "",errors.New("节点不存在")}
 cfg,err:=readPublicPorts(configPath);if err!=nil{return "",err}
 if publicPort==0||publicPort==listenPort{
  if _,exists:=cfg.Nodes[tag];!exists{return "",errors.New("当前节点没有对外端口覆盖")}
  delete(cfg.Nodes,tag)
 }else{
  if previous,ok:=cfg.Nodes[tag];ok&&previous.ListenPort==listenPort&&previous.PublicPort==publicPort{return "",errors.New("对外端口未改变")}
  cfg.Nodes[tag]=publicPortEntry{ListenPort:listenPort,PublicPort:publicPort}
 }
 encoded,err:=json.MarshalIndent(cfg,"","  ");if err!=nil{return "",err}
 if err=writeAtomically(publicPortsPath(configPath),append(encoded,10),0600);err!=nil{return "",err}
 if publicPort==0||publicPort==listenPort{
  return fmt.Sprintf("已清除 %s 的对外端口覆盖，导出恢复监听端口 %d；无需重启 Xray。",tag,listenPort),nil
 }
 return fmt.Sprintf("已设置 %s：对外 %d → 回源 %d。仅影响分享链接；必须自行配置 Cloudflare Origin Rule，且无需重启 Xray。",tag,publicPort,listenPort),nil
}
