package main

import (
 "encoding/json"
 "errors"
 "fmt"
 "net"
 "regexp"
 "strconv"
 "strings"
)

// NodeChange only permits explicitly supported protocol settings.
type NodeChange struct { Kind string; Tag string; Value string }
var safeXHTTPPath=regexp.MustCompile(`^/[A-Za-z0-9/_~.-]{0,127}$`)
var safeDNSLabel=regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
var safeXHTTPHost=regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var safeXHTTPPadding=regexp.MustCompile(`^([0-9]{1,5})-([0-9]{1,5})$`)
var safeRealityShortID=regexp.MustCompile(`^[0-9a-fA-F]{2}(?:[0-9a-fA-F]{2}){0,7}$`)

func validateXHTTPHost(value string) error {
 value=strings.TrimSpace(value)
 if value==""{return nil}
 if len(value)>253{return errors.New("Host is too long")}
 if strings.ContainsAny(value,"/:@ \t\r\n"){return errors.New("Host must be a DNS name or IP without scheme, path, port, or credentials")}
 if net.ParseIP(value)!=nil{return nil}
 if !safeXHTTPHost.MatchString(value){return errors.New("Invalid DNS hostname")}
 for _,label:=range strings.Split(value,".") {
  if label==""||len(label)>63||strings.HasPrefix(label,"-")||strings.HasSuffix(label,"-"){return errors.New("Invalid DNS hostname label")}
 }
 return nil
}

func validateXHTTPPadding(value string) error {
 value=strings.TrimSpace(value)
 matches:=safeXHTTPPadding.FindStringSubmatch(value)
 if matches==nil{return errors.New("Padding must be a range such as 100-1000")}
 start,err:=strconv.Atoi(matches[1]);if err!=nil{return errors.New("Invalid padding start")}
 end,err:=strconv.Atoi(matches[2]);if err!=nil{return errors.New("Invalid padding end")}
 if start>65535||end>65535{return errors.New("Padding values must not exceed 65535")}
 if start>end{return errors.New("Padding start must not exceed the end")}
 return nil
}

func validateRealityShortID(value string) error {
 value=strings.TrimSpace(value)
 if value==""{return nil}
 if !safeRealityShortID.MatchString(value){return errors.New("Short ID must contain 1–8 bytes as even-length hexadecimal")}
 return nil
}

func validateListenPort(value string) (int,error) {
 port,err:=strconv.Atoi(strings.TrimSpace(value))
 if err!=nil||port<1||port>65535{return 0,errors.New("Listener port must be between 1 and 65535")}
 return port,nil
}

func inboundPortValue(value any) int {
 switch v:=value.(type) {
 case float64: return int(v)
 case json.Number: n,err:=v.Int64();if err==nil{return int(n)}
 case int: return v
 }
 return 0
}

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
 case "port":
  port,err:=validateListenPort(change.Value);if err!=nil{return "",err}
  if inboundPortValue(target["port"])==port{return "",errors.New("Listener port unchanged")}
  if raw,ok:=root["inbounds"].([]any);ok {
   for _,value:=range raw {
    other,ok:=getMap(value);if !ok||other["tag"]==change.Tag{continue}
    if inboundPortValue(other["port"])==port {
     return "",fmt.Errorf("Port %d is already used by inbound %v",port,other["tag"])
    }
   }
  }
  target["port"]=port
  return fmt.Sprintf("Inbound %s listener port changed to %d. Open %d/TCP in the firewall or security group, update any CDN Origin Rule, and reconfigure the public-port mapping. Xray was restarted after validation.",change.Tag,port,port),nil
 case "xhttp_host":
  if network!="xhttp"{return "",errors.New("Only XHTTP host edits are supported")}
  settings,ok:=getMap(stream["xhttpSettings"]);if !ok{return "",errors.New("Invalid XHTTP inbound configuration")}
  value:=strings.TrimSpace(change.Value)
  if err:=validateXHTTPHost(value);err!=nil{return "",err}
  current,_:=settings["host"].(string)
  if current==value{return "",errors.New("XHTTP host unchanged")}
  if value=="" {
   delete(settings,"host")
   return fmt.Sprintf("Inbound %s XHTTP host cleared. Clients that pinned the old host must update their link.",change.Tag),nil
  }
  settings["host"]=value
  return fmt.Sprintf("Inbound %s XHTTP host changed to %s. Clients that pinned the old host must reimport the link.",change.Tag,value),nil
 case "xhttp_padding":
  if network!="xhttp"{return "",errors.New("Only XHTTP padding edits are supported")}
  settings,ok:=getMap(stream["xhttpSettings"]);if !ok{return "",errors.New("Invalid XHTTP inbound configuration")}
  value:=strings.TrimSpace(change.Value)
  if err:=validateXHTTPPadding(value);err!=nil{return "",err}
  extra,ok:=getMap(settings["extra"])
  if !ok&&settings["extra"]!=nil{return "",errors.New("XHTTP extra is not an object")}
  if extra==nil{extra=map[string]any{};settings["extra"]=extra}
  if current,_:=extra["xPaddingBytes"].(string);current==value{return "",errors.New("Padding unchanged")}
  extra["xPaddingBytes"]=value
  // Remove the legacy top-level copy so exports and Xray read one value.
  delete(settings,"xPaddingBytes")
  return fmt.Sprintf("Inbound %s XHTTP padding changed to %s. Clients must reimport the link to use the new range.",change.Tag,value),nil
 case "reality_short_id":
  if security!="reality"{return "",errors.New("Only REALITY short-ID edits are supported")}
  settings,ok:=getMap(stream["realitySettings"]);if !ok{return "",errors.New("Invalid REALITY configuration")}
  shortIDs,ok:=settings["shortIds"].([]any)
  if !ok||len(shortIDs)!=1{return "",errors.New("Inbounds with multiple or missing short IDs must be edited locally")}
  current,ok:=shortIDs[0].(string)
  if !ok{return "",errors.New("Original short ID is invalid")}
  if err:=validateRealityShortID(change.Value);err!=nil{return "",err}
  if strings.EqualFold(current,change.Value){return "",errors.New("Short ID unchanged")}
  settings["shortIds"]=[]any{strings.TrimSpace(change.Value)}
  return fmt.Sprintf("REALITY inbound %s short ID changed. Clients must update sid or reimport the link.",change.Tag),nil
 default:
  return "",errors.New("This inbound operation is not on the safety allowlist")
 }
}

func ChangeNodeConfig(configPath,xrayBinary string,change NodeChange) (string,error) {
 return commitManagedConfig(configPath,xrayBinary,func(root map[string]any)(string,error){
  return applyManagedNodeChange(root,change)
 })
}
