package main

import (
 "archive/zip"
 "bytes"
 "crypto/sha256"
 "encoding/hex"
 "fmt"
 "testing"
)

func TestVerifiedDigestFormats(t *testing.T) {
 payload:=[]byte("test release payload")
 sum:=sha256.Sum256(payload)
 h:=hex.EncodeToString(sum[:])
 for _,text:=range []string{h+"  binary-amd64\n","SHA256= "+h+"\n","SHA2-256= "+h+"\n"} {
  if err:=verifiedDigest(payload,[]byte(text),"binary-amd64");err!=nil{t.Errorf("digest %q: %v",text,err)}
 }
 if err:=verifiedDigest(payload,[]byte("SHA256= "+h),"wrong-file");err!=nil{ // .dgst has no filename
  t.Fatal(err)
 }
 if err:=verifiedDigest([]byte("tampered"),[]byte("SHA256= "+h),"binary-amd64");err==nil{t.Fatal("tampering accepted")}
 if err:=verifiedDigest(payload,[]byte(h+"  another-file"),"binary-amd64");err==nil{t.Fatal("wrong filename accepted")}
}
func TestExtractCoreExecutable(t *testing.T) {
 var buf bytes.Buffer
 w:=zip.NewWriter(&buf)
 f,err:=w.Create("xray");if err!=nil{t.Fatal(err)}
 f.Write([]byte{0x7f,'E','L','F',1,2,3})
 if err=w.Close();err!=nil{t.Fatal(err)}
 data,err:=extractCoreExecutable(buf.Bytes())
 if err!=nil{t.Fatal(err)}
 if string(data[:4])!=string([]byte{0x7f,'E','L','F'}){t.Fatal("invalid extracted ELF")}
 if _,err=extractCoreExecutable([]byte("not zip"));err==nil{t.Fatal("invalid zip accepted")}
}
func TestRemoteUpdateVersionSafety(t *testing.T) {
 for _,tag:=range []string{"v26.10.29","v26.10","v1.0.0"} {if !safeTag.MatchString(tag){t.Fatal("valid tag rejected",tag)}}
 for _,tag:=range []string{"v1/../x","../main","latest","v1.0.0/evil"} {if safeTag.MatchString(tag){t.Fatal(fmt.Sprint("unsafe tag accepted: ",tag))}}
}
