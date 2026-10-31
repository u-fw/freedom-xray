package main

import (
 "bytes"
 "crypto/ecdsa"
 "crypto/elliptic"
 "crypto/rand"
 "crypto/tls"
 "crypto/x509"
 "crypto/x509/pkix"
 "encoding/json"
 "encoding/pem"
 "errors"
 "fmt"
 "log"
 "math/big"
 "net"
 "os"
 "path/filepath"
 "regexp"
 "strings"
 "time"
)

// NodeTemplate identifies the three VLESS transports the Bot can create.
// VMess, Trojan and Shadowsocks are intentionally out of scope.
type NodeTemplate string

const (
 NodeTemplateTCPReality NodeTemplate="tcp-reality"
 NodeTemplateXHTTPReality NodeTemplate="xhttp-reality"
 NodeTemplateXHTTPTLS NodeTemplate="xhttp-tls"
)

func validNodeTemplate(value NodeTemplate) bool {
 switch value {
 case NodeTemplateTCPReality,NodeTemplateXHTTPReality,NodeTemplateXHTTPTLS:return true
 }
 return false
}

func nodeTemplateName(value NodeTemplate) string {
 switch value {
 case NodeTemplateTCPReality:return "VLESS + TCP + REALITY + Vision"
 case NodeTemplateXHTTPReality:return "VLESS + XHTTP + REALITY"
 case NodeTemplateXHTTPTLS:return "VLESS + XHTTP + TLS (CDN)"
 }
 return string(value)
}

const (
 defaultNodeXHTTPPadding="100-1000"
 defaultNodeMaxTimeDiff int64=60000
)

var safeCertDomain=regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var vlessEncField=regexp.MustCompile(`"?(decryption|encryption)"?\s*:\s*"([^"]+)"`)

// NodeTemplateParams is the complete, explicit input for one new inbound.
// Empty optional values are filled with the same defaults freedom.sh uses.
type NodeTemplateParams struct {
 Tag string
 Port int
 Email string
 UUID string
 SNI string
 PrivateKey string
 ShortID string
 Decryption string
 MLDSA65Seed string
 Path string
 Mode string
 XHTTPHost string
 XHTTPPadding string
 Vision bool
 CertFile string
 KeyFile string
 ALPN []string
 TrustedXForwardedFor []string
 Dest string
 Target string
 Xver int
 MaxTimeDiff int64
}

func nodeClientList(p NodeTemplateParams) []any {
 if strings.TrimSpace(p.UUID)==""{return []any{}}
 email:=strings.TrimSpace(p.Email)
 if email==""{email="unbound-template"}
 client:=map[string]any{"id":p.UUID,"email":email}
 if p.Vision {client["flow"]="xtls-rprx-vision"}
 return []any{client}
}

func nodeSockopt(trustedXFF []string) map[string]any {
 sockopt:=map[string]any{
  "tcpFastOpen":true,
  "tcpCongestion":"bbr",
  "tcpNoDelay":true,
  "tcpUserTimeout":10000,
  "tcpKeepAliveIdle":45,
  "tcpKeepAliveInterval":15,
 }
 if len(trustedXFF)>0 {
  values:=make([]any,0,len(trustedXFF))
  for _,v:=range trustedXFF{values=append(values,v)}
  sockopt["trustedXForwardedFor"]=values
 }
 return sockopt
}

func nodeSniffing() map[string]any {
 return map[string]any{
  "enabled":true,
  "destOverride":[]any{"http","tls","quic"},
  "routeOnly":true,
 }
}

func realityTarget(p NodeTemplateParams) string {
 if strings.TrimSpace(p.Target)!=""{return strings.TrimSpace(p.Target)}
 if strings.TrimSpace(p.Dest)!=""{return strings.TrimSpace(p.Dest)}
 return p.SNI+":443"
}

func realityDest(p NodeTemplateParams) string {
 if strings.TrimSpace(p.Dest)!=""{return strings.TrimSpace(p.Dest)}
 return realityTarget(p)
}

func normalizedNodeParams(p NodeTemplateParams) NodeTemplateParams {
 if strings.TrimSpace(p.Email)==""{p.Email="unbound-template"}
 if p.Xver<0||p.Xver>3{p.Xver=0}
 if p.MaxTimeDiff<=0{p.MaxTimeDiff=defaultNodeMaxTimeDiff}
 if strings.TrimSpace(p.Decryption)==""{p.Decryption="none"}
 p.SNI=strings.TrimSpace(p.SNI)
 p.Tag=strings.TrimSpace(p.Tag)
 p.ShortID=strings.TrimSpace(p.ShortID)
 p.Path=strings.TrimSpace(p.Path)
 p.Mode=strings.TrimSpace(p.Mode)
 p.XHTTPHost=strings.TrimSpace(p.XHTTPHost)
 p.XHTTPPadding=strings.TrimSpace(p.XHTTPPadding)
 if p.XHTTPPadding==""{p.XHTTPPadding=defaultNodeXHTTPPadding}
 if p.Mode==""{p.Mode="auto"}
 if len(p.ALPN)==0{p.ALPN=[]string{"h2","http/1.1"}}
 if len(p.TrustedXForwardedFor)==0{p.TrustedXForwardedFor=[]string{"CF-Connecting-IP"}}
 p.CertFile=strings.TrimSpace(p.CertFile)
 p.KeyFile=strings.TrimSpace(p.KeyFile)
 return p
}

func validateNodeTag(tag string) error {
 if !managedUserName.MatchString(tag){return errors.New("Tag must use 1–48 letters, digits, _, ., @, or -")}
 if strings.Contains(tag,"+"){return errors.New("Tag must not contain '+'")}
 return nil
}

func validateNodeXHTTPMode(mode string) error {
 switch mode {
 case "auto","packet-up","stream-up","stream-one":return nil
 }
 return errors.New("Unsupported XHTTP mode")
}

func validateNodeALPN(values []string) error {
 if len(values)==0{return errors.New("ALPN must not be empty")}
 for _,v:=range values {
  switch v {
  case "h2","http/1.1":continue
  }
  return fmt.Errorf("Unsupported ALPN value %q",v)
 }
 return nil
}

// ValidateNodeTemplateParams validates everything that does not require the
// certificate files to exist yet. CreateNode validates certificate material.
func ValidateNodeTemplateParams(template NodeTemplate,p NodeTemplateParams) error {
 p=normalizedNodeParams(p)
 if !validNodeTemplate(template){return errors.New("Unknown inbound template")}
 if err:=validateNodeTag(p.Tag);err!=nil{return err}
 if p.Port<1||p.Port>65535{return errors.New("Listener port must be between 1 and 65535")}
 switch template {
 case NodeTemplateTCPReality,NodeTemplateXHTTPReality:
  if err:=validateRealitySNI(p.SNI);err!=nil{return err}
  if strings.TrimSpace(p.PrivateKey)==""{return errors.New("REALITY private key is required")}
  if err:=validateRealityShortID(p.ShortID);err!=nil{return err}
  if strings.TrimSpace(p.Decryption)==""{p.Decryption="none"}
 case NodeTemplateXHTTPTLS:
  if err:=validateRealitySNI(p.SNI);err!=nil{return err}
  if p.CertFile==""||p.KeyFile==""{return errors.New("TLS certificate and key paths are required")}
  if !filepath.IsAbs(p.CertFile)||!filepath.IsAbs(p.KeyFile){return errors.New("TLS certificate and key paths must be absolute")}
  if err:=validateNodeALPN(p.ALPN);err!=nil{return err}
 }
 if template==NodeTemplateXHTTPReality||template==NodeTemplateXHTTPTLS {
  if !safeXHTTPPath.MatchString(p.Path){return errors.New("Path must begin with / and contain up to 128 letters, digits, /, _, -, ., or ~")}
  if err:=validateNodeXHTTPMode(p.Mode);err!=nil{return err}
  if err:=validateXHTTPHost(p.XHTTPHost);err!=nil{return err}
  if err:=validateXHTTPPadding(p.XHTTPPadding);err!=nil{return err}
 }
 return nil
}

func buildRealitySettings(p NodeTemplateParams) map[string]any {
 settings:=map[string]any{
  "show":false,
  "dest":realityDest(p),
  "target":realityTarget(p),
  "xver":p.Xver,
  "serverNames":[]any{p.SNI},
  "privateKey":p.PrivateKey,
  "shortIds":[]any{p.ShortID},
  "maxTimeDiff":p.MaxTimeDiff,
 }
 if strings.TrimSpace(p.MLDSA65Seed)!=""{settings["mldsa65Seed"]=strings.TrimSpace(p.MLDSA65Seed)}
 return settings
}

func buildXHTTPSettings(p NodeTemplateParams) map[string]any {
 settings:=map[string]any{
  "path":p.Path,
  "mode":p.Mode,
  "extra":map[string]any{"xPaddingBytes":p.XHTTPPadding},
 }
 if p.XHTTPHost!=""{settings["host"]=p.XHTTPHost}
 return settings
}

// BuildNodeInbound returns a fresh Xray inbound object for a template. It
// never mutates shared state. A caller that creates a real node passes an
// empty UUID so settings.clients stays empty, matching freedom.sh.
func BuildNodeInbound(template NodeTemplate,p NodeTemplateParams) (map[string]any,error) {
 p=normalizedNodeParams(p)
 if err:=ValidateNodeTemplateParams(template,p);err!=nil{return nil,err}
 settings:=map[string]any{
  "clients":nodeClientList(p),
  "decryption":p.Decryption,
 }
 inbound:=map[string]any{
  "tag":p.Tag,
  "port":p.Port,
  "protocol":"vless",
  "settings":settings,
  "sniffing":nodeSniffing(),
 }
 switch template {
 case NodeTemplateTCPReality:
  inbound["streamSettings"]=map[string]any{
   "network":"tcp",
   "security":"reality",
   "realitySettings":buildRealitySettings(p),
   "sockopt":nodeSockopt(nil),
  }
 case NodeTemplateXHTTPReality:
  inbound["streamSettings"]=map[string]any{
   "network":"xhttp",
   "security":"reality",
   "realitySettings":buildRealitySettings(p),
   "xhttpSettings":buildXHTTPSettings(p),
   "sockopt":nodeSockopt(nil),
  }
 case NodeTemplateXHTTPTLS:
  alpn:=make([]any,0,len(p.ALPN))
  for _,v:=range p.ALPN{alpn=append(alpn,v)}
  inbound["streamSettings"]=map[string]any{
   "network":"xhttp",
   "security":"tls",
   "tlsSettings":map[string]any{
    // Xray accepts serverName on an inbound TLS config and ignores it at
    // runtime; the Bot uses it to keep the SNI stable when the certificate
    // path does not encode the domain.
    "serverName":p.SNI,
    "certificates":[]any{map[string]any{"certificateFile":p.CertFile,"keyFile":p.KeyFile}},
    "alpn":alpn,
   },
   "xhttpSettings":buildXHTTPSettings(p),
   "sockopt":nodeSockopt(p.TrustedXForwardedFor),
  }
 default:
  return nil,errors.New("Unknown inbound template")
 }
 return inbound,nil
}

func randomHexBytes(count int) (string,error) {
 if count<1||count>64{return "",errors.New("Invalid random byte count")}
 raw:=make([]byte,count)
 if _,err:=rand.Read(raw);err!=nil{return "",err}
 return fmt.Sprintf("%x",raw),nil
}

func randomShortID() (string,error) {return randomHexBytes(8)}
func randomXHTTPPath() (string,error) {
 raw:=make([]byte,16)
 if _,err:=rand.Read(raw);err!=nil{return "",err}
 return fmt.Sprintf("/%x",raw),nil
}

// generateRealityKeyPair creates the server X25519 key and its public peer.
func generateRealityKeyPair() (string,string,error) {
 out,err:=runXrayHelper("x25519")
 if err!=nil{return "","",err}
 priv:=strings.TrimSpace(fieldAfterColon(out,"PrivateKey","Private key","Private"))
 pub:=strings.TrimSpace(fieldAfterColon(out,"Password","PublicKey","Public key","Public"))
 if priv==""||pub==""{return "","",errors.New("Unable to parse xray x25519 output")}
 return priv,pub,nil
}

func existingRealityPrivateKey(root map[string]any) string {
 inbounds,ok:=root["inbounds"].([]any)
 if !ok{return ""}
 for _,raw:=range inbounds {
  in,ok:=getMap(raw);if !ok{continue}
  stream,ok:=getMap(in["streamSettings"]);if !ok{continue}
  if stream["security"]!="reality"{continue}
  reality,ok:=getMap(stream["realitySettings"]);if !ok{continue}
  if key,ok:=reality["privateKey"].(string);ok&&strings.TrimSpace(key)!=""{return strings.TrimSpace(key)}
 }
 return ""
}

// resolveRealityPrivateKey prefers the shared key already used by REALITY
// inbounds, matching freedom.sh, and only generates one when none exists.
func resolveRealityPrivateKey(configPath string) (string,error) {
 if data,err:=os.ReadFile(configPath);err==nil {
  var root map[string]any
  decoder:=json.NewDecoder(bytes.NewReader(data));decoder.UseNumber()
  if decoder.Decode(&root)==nil {
   if key:=existingRealityPrivateKey(root);key!=""{return key,nil}
  }
 }
 private,_,err:=generateRealityKeyPair()
 return private,err
}

// generateMLDSA65Seed creates a REALITY ML-DSA-65 seed.
func generateMLDSA65Seed() (string,error) {
 out,err:=runXrayHelper("mldsa65")
 if err!=nil{return "",err}
 seed:=strings.Join(strings.Fields(fieldAfterColon(out,"Seed")),"")
 if seed==""{return "",errors.New("Unable to parse xray mldsa65 output")}
 return seed,nil
}

// generateVLESSEncryptionPair creates the server decryption and paired client
// encryption strings for ML-KEM-768. It accepts both JSON and labelled text.
func generateVLESSEncryptionPair() (string,string,error) {
 out,err:=runXrayHelper("vlessenc")
 if err!=nil{return "","",err}
 var structured map[string]map[string]any
 if json.Unmarshal([]byte(out),&structured)==nil {
  if values,ok:=structured["ML-KEM-768"];ok {
   dec,_:=values["decryption"].(string)
   enc,_:=values["encryption"].(string)
   if dec!=""&&enc!=""{return dec,enc,nil}
  }
 }
 inSection:=false
 dec,enc:="",""
 for _,line:=range strings.Split(out,"\n") {
  if strings.Contains(line,"ML-KEM-768"){inSection=true}
  if !inSection{continue}
  matches:=vlessEncField.FindStringSubmatch(line)
  if matches==nil{continue}
  if matches[1]=="decryption"{dec=matches[2]}else if matches[1]=="encryption"{enc=matches[2]}
 }
 if dec==""||enc==""{return "","",errors.New("Unable to parse xray vlessenc output")}
 return dec,enc,nil
}

func certsDirectory(configPath string) string {
 return filepath.Join(filepath.Dir(configPath),"certs")
}

// GenerateSelfSignedCertificatePEM creates a 10-year ECC P-256 certificate
// with the domain as CN and SAN. It does not write to disk.
func GenerateSelfSignedCertificatePEM(domain string) ([]byte,[]byte,error) {
 domain=strings.TrimSpace(domain)
 if err:=validateRealitySNI(domain);err!=nil{return nil,nil,err}
 key,err:=ecdsa.GenerateKey(elliptic.P256(),rand.Reader)
 if err!=nil{return nil,nil,err}
 serialLimit:=new(big.Int).Lsh(big.NewInt(1),128)
 serial,err:=rand.Int(rand.Reader,serialLimit)
 if err!=nil{return nil,nil,err}
 now:=time.Now()
 template:=&x509.Certificate{
  SerialNumber:serial,
  Subject:pkix.Name{CommonName:domain},
  NotBefore:now.Add(-5*time.Minute),
  NotAfter:now.AddDate(10,0,0),
  KeyUsage:x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment,
  ExtKeyUsage:[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
  BasicConstraintsValid:true,
  DNSNames:[]string{domain},
 }
 if ip:=net.ParseIP(domain);ip!=nil {
  template.IPAddresses=[]net.IP{ip}
  template.DNSNames=nil
 }
 der,err:=x509.CreateCertificate(rand.Reader,template,template,&key.PublicKey,key)
 if err!=nil{return nil,nil,err}
 keyDER,err:=x509.MarshalECPrivateKey(key)
 if err!=nil{return nil,nil,err}
 var certBuf,keyBuf bytes.Buffer
 if err=pem.Encode(&certBuf,&pem.Block{Type:"CERTIFICATE",Bytes:der});err!=nil{return nil,nil,err}
 if err=pem.Encode(&keyBuf,&pem.Block{Type:"EC PRIVATE KEY",Bytes:keyDER});err!=nil{return nil,nil,err}
 return certBuf.Bytes(),keyBuf.Bytes(),nil
}

// ValidateCertificatePair parses PEM files, proves the key matches the
// certificate, and optionally checks that the certificate covers a domain.
func ValidateCertificatePair(certPath,keyPath,domain string) error {
 certPEM,err:=os.ReadFile(certPath)
 if err!=nil{return fmt.Errorf("read certificate: %w",err)}
 keyPEM,err:=os.ReadFile(keyPath)
 if err!=nil{return fmt.Errorf("read private key: %w",err)}
 return validateCertificatePEM(certPEM,keyPEM,domain)
}

func validateCertificatePEM(certPEM,keyPEM []byte,domain string) error {
 pair,err:=tls.X509KeyPair(certPEM,keyPEM)
 if err!=nil{return fmt.Errorf("certificate and key do not match: %w",err)}
 leaf,err:=x509.ParseCertificate(pair.Certificate[0])
 if err!=nil{return fmt.Errorf("parse certificate: %w",err)}
 if domain==""{return nil}
 if err:=leaf.VerifyHostname(domain);err!=nil{return fmt.Errorf("certificate does not cover %s: %w",domain,err)}
 return nil
}

func metaPath(configPath string) string {
 return filepath.Join(filepath.Dir(configPath),"freedom-meta.json")
}

func metaSetClientEncryptionSidecar(configPath,tag,encryption string) configSidecar {
 return configSidecar{
  Path:metaPath(configPath),
  Transform:func(original []byte,exists bool)([]byte,error){
   if !exists&&(encryption==""||encryption=="none"){return nil,nil}
   root:=map[string]any{}
   if exists&&len(bytes.TrimSpace(original))>0 {
    if err:=json.Unmarshal(original,&root);err!=nil{return nil,fmt.Errorf("freedom-meta.json is corrupted: %w",err)}
   }
   nodes,ok:=getMap(root["nodes"])
   if !ok {
    if root["nodes"]!=nil{return nil,errors.New("freedom-meta.json nodes entry is not an object")}
    nodes=map[string]any{}
   }
   entry,ok:=getMap(nodes[tag])
   if !ok {
    if nodes[tag]!=nil{return nil,fmt.Errorf("freedom-meta.json entry for %s is not an object",tag)}
    entry=map[string]any{}
   }
   if encryption==""||encryption=="none" {
    delete(entry,"client_encryption")
    if len(entry)==0{delete(nodes,tag)}else{nodes[tag]=entry}
   }else{
    entry["client_encryption"]=encryption
    nodes[tag]=entry
   }
   root["nodes"]=nodes
   encoded,err:=json.MarshalIndent(root,"","  ")
   if err!=nil{return nil,err}
   return append(encoded,'\n'),nil
  },
 }
}

func publicPortsDeleteSidecar(configPath,tag string) configSidecar {
 return configSidecar{
  Path:publicPortsPath(configPath),
  Transform:func(original []byte,exists bool)([]byte,error){
   if !exists{return nil,nil}
   root:=map[string]any{}
   if exists&&len(bytes.TrimSpace(original))>0 {
    if err:=json.Unmarshal(original,&root);err!=nil{return nil,fmt.Errorf("freedom-public-ports.json is corrupted: %w",err)}
   }
   nodes,ok:=getMap(root["nodes"])
   if !ok {
    if root["nodes"]!=nil{return nil,errors.New("freedom-public-ports.json nodes entry is not an object")}
    nodes=map[string]any{}
   }
   delete(nodes,tag)
   root["nodes"]=nodes
   encoded,err:=json.MarshalIndent(root,"","  ")
   if err!=nil{return nil,err}
   return append(encoded,'\n'),nil
  },
 }
}

func fileSidecar(path string,mode os.FileMode,data []byte) configSidecar {
 return configSidecar{
  Path:path,
  Mode:mode,
  BeforeValidation:true,
  CreateOnly:true,
  Transform:func(original []byte,exists bool)([]byte,error){
   if data==nil{return nil,nil}
   return append([]byte(nil),data...),nil
  },
 }
}

// NodeCreateRequest carries generated material that must be committed with
// the config. CertPEM/KeyPEM are non-nil only for a Bot-generated certificate.
type NodeCreateRequest struct {
 Template NodeTemplate
 Params NodeTemplateParams
 ClientEncryption string
 CertPEM []byte
 KeyPEM []byte
}

func uniqueNodeTagPort(root map[string]any,tag string,port int)(bool,error) {
 raw,ok:=root["inbounds"].([]any)
 if !ok{return false,errors.New("Missing inbounds")}
 for _,value:=range raw {
  in,ok:=getMap(value);if !ok{return false,errors.New("Invalid inbound format")}
  if in["tag"]==tag{return false,errors.New("Inbound tag already exists")}
  if inboundPortValue(in["port"])==port{return false,fmt.Errorf("Port %d is already used by inbound %v",port,in["tag"])}
 }
 return true,nil
}

// CreateNode commits a new VLESS inbound, its paired encryption metadata and
// an optional generated certificate as one transaction.
func CreateNode(configPath,xrayBinary,statsServer string,req NodeCreateRequest)(string,error) {
 req.Params=normalizedNodeParams(req.Params)
 if (req.Template==NodeTemplateTCPReality||req.Template==NodeTemplateXHTTPReality)&&strings.TrimSpace(req.Params.PrivateKey)=="" {
  key,err:=resolveRealityPrivateKey(configPath)
  if err!=nil{return "",err}
  req.Params.PrivateKey=key
 }
 if err:=ValidateNodeTemplateParams(req.Template,req.Params);err!=nil{return "",err}
 if req.Template==NodeTemplateXHTTPTLS {
  if len(req.CertPEM)>0||len(req.KeyPEM)>0 {
   if err:=validateCertificatePEM(req.CertPEM,req.KeyPEM,req.Params.SNI);err!=nil{return "",err}
   // A generated self-signed pair must never silently replace an existing
   // certificate, which could belong to acme.sh or another inbound.
   for _,path:=range []string{req.Params.CertFile,req.Params.KeyFile} {
    if _,err:=os.Stat(path);err==nil {
     return "",fmt.Errorf("refusing to overwrite existing certificate file %s; use the existing certificate or choose another domain",path)
    }else if !os.IsNotExist(err){return "",err}
   }
  }else{
   if err:=ValidateCertificatePair(req.Params.CertFile,req.Params.KeyFile,req.Params.SNI);err!=nil{return "",err}
  }
 }
 inbound,err:=BuildNodeInbound(req.Template,req.Params)
 if err!=nil{return "",err}
 sidecars:=[]configSidecar{metaSetClientEncryptionSidecar(configPath,req.Params.Tag,req.ClientEncryption)}
 if len(req.CertPEM)>0 {
  sidecars=append(sidecars,
   fileSidecar(req.Params.CertFile,0644,req.CertPEM),
   fileSidecar(req.Params.KeyFile,0600,req.KeyPEM),
  )
 }
 return commitManagedConfigTx(configPath,xrayBinary,statsServer,sidecars,func(root map[string]any)(string,error){
  if _,err:=uniqueNodeTagPort(root,req.Params.Tag,req.Params.Port);err!=nil{return "",err}
  raw,ok:=root["inbounds"].([]any)
  if !ok{return "",errors.New("Missing inbounds")}
  root["inbounds"]=append(raw,inbound)
  note:=""
  if len(req.CertPEM)>0{note=" A 10-year self-signed certificate was generated in the certs directory."}
  return fmt.Sprintf("Created inbound %s (%s) on port %d.%s It has no assigned users; assign one before connecting. Open %d/TCP in the firewall or security group, and configure any CDN Origin Rule.",req.Params.Tag,nodeTemplateName(req.Template),req.Params.Port,note,req.Params.Port),nil
 })
}

type nodeDeletionReport struct {
 Tag string
 Port int
 Clients int
 RoutingRules []int
 SuspendedUsers []string
 PublicPort int
 Found bool
}

func inspectNodeDeletion(configPath,tag string)(nodeDeletionReport,error) {
 report:=nodeDeletionReport{Tag:tag}
 data,err:=os.ReadFile(configPath);if err!=nil{return report,err}
 var root map[string]any
 decoder:=json.NewDecoder(bytes.NewReader(data));decoder.UseNumber()
 if err=decoder.Decode(&root);err!=nil{return report,err}
 inbounds,ok:=root["inbounds"].([]any)
 if !ok{return report,errors.New("Missing inbounds")}
 for _,value:=range inbounds {
  in,ok:=getMap(value);if !ok{continue}
  if in["tag"]!=tag{continue}
  if report.Found{return report,errors.New("Duplicate inbound tag; refusing deletion")}
  report.Found=true
  report.Port=inboundPortValue(in["port"])
  if settings,ok:=getMap(in["settings"]);ok {
   if clients,ok:=settings["clients"].([]any);ok{report.Clients=len(clients)}
  }
 }
 if !report.Found{return report,errors.New("Inbound not found")}
 if routing,ok:=getMap(root["routing"]);ok {
  if rules,ok:=routing["rules"].([]any);ok {
   for i,value:=range rules {
    rule,ok:=getMap(value);if !ok{continue}
    tags,ok:=rule["inboundTag"].([]any);if !ok{continue}
    for _,rawTag:=range tags {
     if rawTag==tag{report.RoutingRules=append(report.RoutingRules,i);break}
    }
   }
  }
 }
 store,_,storeErr:=readSuspendedStore(configPath)
 if storeErr!=nil{return report,storeErr}
 for name,account:=range store.Users {
  if _,ok:=account.Nodes[tag];ok{report.SuspendedUsers=append(report.SuspendedUsers,name)}
 }
 publicPorts,portErr:=readPublicPorts(configPath)
 if portErr==nil {
  if entry,ok:=publicPorts.Nodes[tag];ok{report.PublicPort=entry.PublicPort}
 }
 return report,nil
}

// DeleteNode removes an inbound and its sidecar metadata. It refuses while a
// suspended account still references the node, because restoring that account
// would otherwise fail or silently drop its parked client.
func DeleteNode(configPath,xrayBinary,statsServer,tag string)(string,error) {
 if tag==""||tag=="api-internal"{return "",errors.New("This inbound cannot be deleted")}
 sidecars:=[]configSidecar{
  metaSetClientEncryptionSidecar(configPath,tag,""),
  publicPortsDeleteSidecar(configPath,tag),
 }
 result,err:=commitManagedConfigTx(configPath,xrayBinary,statsServer,sidecars,func(root map[string]any)(string,error){
  store,_,err:=readSuspendedStore(configPath);if err!=nil{return "",err}
  var suspended []string
  for name,account:=range store.Users {
   if _,ok:=account.Nodes[tag];ok{suspended=append(suspended,name)}
  }
  if len(suspended)>0{return "",fmt.Errorf("Inbound %s is referenced by suspended user(s): %s; restore or remove those records first",tag,strings.Join(suspended,", "))}
  raw,ok:=root["inbounds"].([]any)
  if !ok{return "",errors.New("Missing inbounds")}
  next:=make([]any,0,len(raw))
  found:=false
  port:=0
  for _,value:=range raw {
   in,ok:=getMap(value);if !ok{return "",errors.New("Invalid inbound format")}
   if in["tag"]==tag {
    if found{return "",errors.New("Duplicate inbound tag; refusing deletion")}
    found=true
    port=inboundPortValue(in["port"])
    continue
   }
   next=append(next,value)
  }
  if !found{return "",errors.New("Inbound not found")}
  root["inbounds"]=next
  return fmt.Sprintf("Deleted inbound %s (listener port %d). Metadata was cleaned; the firewall rule and the same-port CDN Origin Rule were not changed. Assignments on other inbounds remain active.",tag,port),nil
 })
 if err!=nil{return result,err}
 // History stays in the per-user totals and the binding ledger, but a later
 // node reusing this tag must start from a zero runtime counter or its first
 // sample would undercount.
 if err:=resetNodeBindingCounters(configPath,tag);err!=nil {
  log.Printf("[Node] binding counter cleanup for %s failed: %v",tag,err)
 }
 return result,nil
}
