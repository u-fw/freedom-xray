package main

import (
 "errors"
 "fmt"
 "regexp"
 "strings"
)

// NodeChange only permits explicitly supported protocol settings.
type NodeChange struct { Kind string; Tag string; Value string }
var safeXHTTPPath=regexp.MustCompile(`^/[A-Za-z0-9/_~.-]{0,127}$`)
var safeDNSLabel=regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func validateRealitySNI(value string) error {
 if len(value)>253||len(value)<3||strings.HasSuffix(value,".") {return errors.New("Invalid DNS hostname length")}
 labels:=strings.Split(value,".")
 if len(labels)<2 {return errors.New("Enter a fully qualified DNS hostname")}
 for _,label:=range labels {
  if !safeDNSLabel.MatchString(label){return errors.New("Only ASCII DNS hostnames are supported; use punycode for IDNs")}
 }
 if !regexp.MustCompile(`^[A-Za-z]`).MatchString(labels[len(labels)-1]){return errors.New("Invalid top-level domain format")}
 return nil
}

func applyManagedNodeChange(root map[string]any,change NodeChange) (string,error) {
 inbounds,err:=usableInbounds(root);if err!=nil{return "",err}
 var target map[string]any
 for _,in:=range inbounds {
  if in["tag"]==change.Tag {
   if target!=nil{return "",errors.New("Duplicate inbound tag; operation rejected")}
   target=in
  }
 }
 if target==nil{return "",errors.New("Target inbound not found")}
 stream,ok:=getMap(target["streamSettings"]);if !ok{return "",errors.New("Invalid inbound streamSettings")}
 network,_:=stream["network"].(string)
 security,_:=stream["security"].(string)
 switch change.Kind {
 case "reality_sni":
  if security!="reality"{return "",errors.New("Only REALITY SNI edits are supported")}
  if err:=validateRealitySNI(change.Value);err!=nil{return "",err}
  settings,ok:=getMap(stream["realitySettings"]);if !ok{return "",errors.New("Invalid REALITY configuration")}
  names,ok:=settings["serverNames"].([]any)
  if !ok||len(names)!=1{return "",errors.New("Inbounds with multiple or missing SNI values must be edited locally")}
  old,ok:=names[0].(string)
  if !ok||old==""{return "",errors.New("Original SNI is invalid")}
  if strings.EqualFold(old,change.Value){return "",errors.New("SNI unchanged")}
  settings["serverNames"]=[]any{change.Value}
  // Only rewrite the standard generated target. Never clobber customized targets.
  oldTarget:=old+":443"
  newTarget:=change.Value+":443"
  adjusted:=false
  for _,field:=range []string{"target","dest"} {
   if current,ok:=settings[field].(string);ok&&strings.EqualFold(current,oldTarget) {
    settings[field]=newTarget
    adjusted=true
   }
  }
  suffix:="Custom target/dest preserved. Verify the certificate matches the new SNI."
  if adjusted {suffix="Default target/dest updated."}
  return fmt.Sprintf("REALITY inbound %s: SNI changed to %s. %s Clients must update SNI.",change.Tag,change.Value,suffix),nil
 case "path","mode":
  if network!="xhttp"{return "",errors.New("Only XHTTP path and mode edits are supported")}
  settings,ok:=getMap(stream["xhttpSettings"]);if !ok{return "",errors.New("Invalid XHTTP inbound configuration")}
  if change.Kind=="path"{
   if !safeXHTTPPath.MatchString(change.Value){return "",errors.New("Path must begin with / and contain up to 128 letters, digits, /, _, -, ., or ~")}
   if settings["path"]==change.Value{return "",errors.New("Path unchanged")}
   settings["path"]=change.Value
   return fmt.Sprintf("Inbound %s XHTTP path changed to %s. Clients must be updated.",change.Tag,change.Value),nil
  }
  switch change.Value {case "auto","packet-up","stream-up","stream-one":default:return "",errors.New("Unsupported XHTTP mode")}
  if settings["mode"]==change.Value{return "",errors.New("Mode unchanged")}
  settings["mode"]=change.Value
  return fmt.Sprintf("Inbound %s XHTTP mode changed to %s. Some clients must reimport the link.",change.Tag,change.Value),nil
 default:
  return "",errors.New("This inbound operation is not on the safety allowlist")
 }
}

func ChangeNodeConfig(configPath,xrayBinary string,change NodeChange) (string,error) {
 return commitManagedConfig(configPath,xrayBinary,func(root map[string]any)(string,error){
  return applyManagedNodeChange(root,change)
 })
}
