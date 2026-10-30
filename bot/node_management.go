package main

import (
 "errors"
 "fmt"
 "regexp"
)

// NodeChange exposes only a narrow allowlist of XHTTP settings.
type NodeChange struct { Kind string; Tag string; Value string }
var safeXHTTPPath=regexp.MustCompile(`^/[A-Za-z0-9/_~.-]{0,127}$`)

func applyManagedNodeChange(root map[string]any,change NodeChange) (string,error) {
 inbounds,err:=usableInbounds(root);if err!=nil{return "",err}
 var target map[string]any
 for _,in:=range inbounds {
  if in["tag"]==change.Tag {
   if target!=nil{return "",errors.New("入站标签重复，拒绝操作")}
   target=in
  }
 }
 if target==nil{return "",errors.New("目标节点不存在")}
 stream,ok:=getMap(target["streamSettings"]);if !ok{return "",errors.New("节点 streamSettings 无效")}
 if stream["network"]!="xhttp" {return "",errors.New("仅支持修改 XHTTP 入站")}
 settings,ok:=getMap(stream["xhttpSettings"]);if !ok{return "",errors.New("节点 XHTTP 配置无效")}
 switch change.Kind {
 case "path":
  if !safeXHTTPPath.MatchString(change.Value){return "",errors.New("Path 必须以 / 开始，只能含字母、数字、/、_、-、.、~，最多 128 字符")}
  if settings["path"]==change.Value{return "",errors.New("Path 没有变化")}
  settings["path"]=change.Value
  return fmt.Sprintf("节点 %s 的 XHTTP Path 已更改为 %s。客户端需要同步更新 Path。",change.Tag,change.Value),nil
 case "mode":
  switch change.Value {case "auto","packet-up","stream-up","stream-one":default:return "",errors.New("不支持此 XHTTP Mode")}
  if settings["mode"]==change.Value{return "",errors.New("Mode 没有变化")}
  settings["mode"]=change.Value
  return fmt.Sprintf("节点 %s 的 XHTTP Mode 已更改为 %s。部分客户端需要重新导入链接。",change.Tag,change.Value),nil
 default:return "",errors.New("仅允许修改 XHTTP Path / Mode")
 }
}

func ChangeNodeConfig(configPath,xrayBinary string,change NodeChange) (string,error) {
 return commitManagedConfig(configPath,xrayBinary,func(root map[string]any)(string,error){
  return applyManagedNodeChange(root,change)
 })
}
