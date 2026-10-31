package main

import (
 "archive/zip"
 "bytes"
 "context"
 "crypto/sha256"
 _ "embed"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net/http"
 "os"
 "os/exec"
 "path/filepath"
 "regexp"
 "runtime"
 "strings"
 "time"
)

//go:embed update-watchdog.sh
var updateWatchdog []byte

const (
 scriptRepo = "u-fw/freedom-xray"
 coreRepo = "XTLS/Xray-core"
 maxUpdateBytes = 80 << 20
)

var updateHTTPClient = &http.Client{
 Timeout: 90*time.Second,
 CheckRedirect: func(req *http.Request,via []*http.Request) error {
  if req.URL.Scheme!="https"{return errors.New("Update downloads may not redirect to non-HTTPS endpoints")}
  if len(via)>8{return errors.New("Too many redirects during update download")}
  return nil
 },
}
var githubSHA = regexp.MustCompile("^[a-f0-9]{40}$")
var botReleaseCommit = regexp.MustCompile("(?m)^Commit: ([a-f0-9]{40})\\s*$")
var botReleaseVersion = regexp.MustCompile("(?m)^Version: (v[0-9]+\\.[0-9]+\\.[0-9]+)\\s*$")
var safeTag = regexp.MustCompile("^v[0-9]+[.][0-9]+([.][0-9]+)?([a-zA-Z0-9.-]+)?$")

type githubRelease struct {
 TagName string `json:"tag_name"`
 Body string `json:"body"`
}
type githubCommit struct {
 SHA string `json:"sha"`
}

func downloadVerifiedHTTPS(ctx context.Context,url string,limit int64) ([]byte,error) {
 if !strings.HasPrefix(url,"https://"){return nil,errors.New("Refusing non-HTTPS download")}
 req,err:=http.NewRequestWithContext(ctx,http.MethodGet,url,nil);if err!=nil{return nil,err}
 resp,err:=updateHTTPClient.Do(req);if err!=nil{return nil,err}
 defer resp.Body.Close()
 if resp.StatusCode!=http.StatusOK{return nil,fmt.Errorf("Download failed (HTTP %d)",resp.StatusCode)}
 data,err:=io.ReadAll(io.LimitReader(resp.Body,limit+1));if err!=nil{return nil,err}
 if int64(len(data))>limit{return nil,errors.New("Downloaded content exceeds the size limit")}
 return data,nil
}

func apiJSON(ctx context.Context,url string,dst any) error {
 data,err:=downloadVerifiedHTTPS(ctx,url,4<<20);if err!=nil{return err}
 return json.Unmarshal(data,dst)
}
func remoteComponentVersion(ctx context.Context,component string) (string,error) {
 switch component {
 case "script":
  var commit githubCommit
  err:=apiJSON(ctx,"https://api.github.com/repos/"+scriptRepo+"/commits/main",&commit)
  if err!=nil{return "",err}
  if !githubSHA.MatchString(commit.SHA){return "",errors.New("Invalid upstream commit SHA")}
  return commit.SHA,nil
 case "bot":
  var release githubRelease
  err:=apiJSON(ctx,"https://api.github.com/repos/"+scriptRepo+"/releases/tags/bot-latest",&release)
  if err!=nil{return "",err}
  if release.TagName!="bot-latest"{return "",errors.New("Unexpected Bot release tag")}
  match:=botReleaseCommit.FindStringSubmatch(release.Body)
  if len(match)==2{
   version:=botReleaseVersion.FindStringSubmatch(release.Body)
   if len(version)==2{return version[1]+" ("+match[1][:12]+")",nil}
   return match[1][:12],nil
  }
  return release.TagName,nil // legacy release without a build commit in its body
 case "core":
  var release githubRelease
  err:=apiJSON(ctx,"https://api.github.com/repos/"+coreRepo+"/releases/latest",&release)
  if err!=nil{return "",err}
  if !safeTag.MatchString(release.TagName){return "",errors.New("Invalid stable Xray version tag")}
  return release.TagName,nil
 }
 return "",errors.New("Unknown update component")
}

func currentComponentVersion(component,xrayBinary string) string {
 switch component {
 case "script":
  b,err:=os.ReadFile("/usr/local/bin/freedom");if err!=nil{return "Not installed"}
  for _,line:=range strings.Split(string(b),"\n") {
   if strings.HasPrefix(line,"SCRIPT_VERSION="){return strings.TrimSpace(line[len("SCRIPT_VERSION="):])}
  }
  return "Unknown"
 case "bot":
  if Commit==""||Commit=="dev"{return Version+" (development)"}
  return Version+" ("+Commit+")"
 case "core":
  out,err:=exec.Command(xrayBinary,"version").Output();if err!=nil{return "Unknown"}
  fields:=strings.Fields(string(out));if len(fields)>1{return fields[1]}
 }
 return "Unknown"
}

func verifiedDigest(payload,checksum []byte,expectedName string) error {
 // GitHub release checksums: either sha256sum format or SHA256=... .dgst.
 var hash string
 for _,line:=range strings.Split(string(checksum),"\n") {
  l:=strings.TrimSpace(line)
  if len(l)==0 {continue}
  kv:=strings.SplitN(l,"=",2)
  if len(kv)==2 && strings.Contains(strings.ToUpper(kv[0]),"256") {
   token:=strings.Fields(strings.TrimSpace(kv[1]))
   if len(token)>0 {hash=token[0];break}
  }
  tokens:=strings.Fields(l)
  if len(tokens)>=2 && strings.TrimPrefix(tokens[1],"*")==expectedName && len(tokens[0])==64 {hash=tokens[0];break}
 }
 if len(hash)!=64{return errors.New("Missing trusted SHA-256 checksum")}
 decoded,err:=hex.DecodeString(strings.ToLower(hash));if err!=nil{return err}
 sum:=sha256.Sum256(payload)
 if !bytes.Equal(sum[:],decoded){return errors.New("SHA-256 verification failed; refusing installation")}
 return nil
}

func archNames() (botArch,coreArch string,err error) {
 if runtime.GOOS!="linux" {return "","",errors.New("Updates are supported only on Linux")}
 switch runtime.GOARCH {
 case "amd64":return "amd64","64",nil
 case "arm64":return "arm64","arm64-v8a",nil
 }
 return "","",errors.New("Unsupported architecture for remote updates")
}

func checkedScriptUpdate(ctx context.Context,commit string) (string,error) {
 if !githubSHA.MatchString(commit){return "",errors.New("Invalid manager script commit SHA")}
 url:="https://raw.githubusercontent.com/"+scriptRepo+"/"+commit+"/freedom.sh"
 script,err:=downloadVerifiedHTTPS(ctx,url,5<<20);if err!=nil{return "",err}
 if !bytes.Contains(script,[]byte("SCRIPT_VERSION=")) || !bytes.HasPrefix(script,[]byte("#!")) {return "",errors.New("Not a valid freedom Shell script")}
 target:="/usr/local/bin/freedom"
 old,err:=os.ReadFile(target);if err!=nil {return "",fmt.Errorf("Install the freedom script locally first: %w",err)}
 tmp,err:=os.CreateTemp(filepath.Dir(target),".freedom-check-*");if err!=nil{return "",err}
 defer os.Remove(tmp.Name())
 if _,err=tmp.Write(script);err!=nil{tmp.Close();return "",err}
 if err=tmp.Close();err!=nil{return "",err}
 if err=runChecked("bash","-n",tmp.Name());err!=nil{return "",fmt.Errorf("Shell syntax check failed: %w",err)}
 if bytes.Equal(old,script){return "Manager script is up to date",nil}
 backup:=target+".previous"
 if err=writeAtomically(backup,old,0700);err!=nil{return "",err}
 if err=writeAtomically(target,script,0755);err!=nil{return "",err}
 return "Updated the freedom script (running Xray is unaffected); backup: "+backup,nil
}

func checkedBotUpdate(ctx context.Context) (string,error) {
 arch,_,err:=archNames();if err!=nil{return "",err}
 name:="freedom-bot-linux-"+arch
 base:="https://github.com/"+scriptRepo+"/releases/download/bot-latest/"+name
 binary,err:=downloadVerifiedHTTPS(ctx,base,maxUpdateBytes);if err!=nil{return "",err}
 checks,err:=downloadVerifiedHTTPS(ctx,base+".sha256",8<<10);if err!=nil{return "",err}
 if err=verifiedDigest(binary,checks,name);err!=nil{return "",err}
 if len(binary)<4||!bytes.Equal(binary[:4],[]byte{0x7f,'E','L','F'}) {return "",errors.New("Downloaded artifact is not an ELF executable")}
 target,err:=os.Executable();if err!=nil{return "",err}
 target,err=filepath.EvalSymlinks(target);if err!=nil{return "",err}
 old,err:=os.ReadFile(target);if err!=nil{return "",err}
 if bytes.Equal(binary,old) {return "Bot is already at the published version",nil}
 tmp,err:=os.CreateTemp(filepath.Dir(target),".freedom-bot-check-*");if err!=nil{return "",err}
 defer os.Remove(tmp.Name())
 if err=tmp.Chmod(0755);err!=nil{tmp.Close();return "",err}
 if _,err=tmp.Write(binary);err!=nil{tmp.Close();return "",err}
 if err=tmp.Close();err!=nil{return "",err}
 if err=runChecked(tmp.Name(),"-v");err!=nil{return "",fmt.Errorf("New Bot binary failed self-check: %w",err)}
 backup:=target+".previous"
 if err=writeAtomically(backup,old,0755);err!=nil{return "",err}
 if err=writeAtomically(target,binary,0755);err!=nil{return "",err}
 // A detached watchdog handles both restart and rollback; it survives even
 // if the replacement cannot launch or authenticate to Telegram.
 watchdog,err:=os.CreateTemp("/run","freedom-bot-watchdog-*.sh")
 if err!=nil{_ =writeAtomically(target,old,0755);return "",fmt.Errorf("Unable to create independent recovery job: %w",err)}
 watchdogPath:=watchdog.Name()
 if err=watchdog.Chmod(0700);err==nil {_,err=watchdog.Write(updateWatchdog)}
 if err==nil {err=watchdog.Sync()}
 if closeErr:=watchdog.Close();err==nil {err=closeErr}
 if err!=nil {os.Remove(watchdogPath);_ =writeAtomically(target,old,0755);return "",fmt.Errorf("Recovery job preparation failed: %w",err)}
 sum:=sha256.Sum256(binary)
 expected:=hex.EncodeToString(sum[:])
 nameUnit:=fmt.Sprintf("freedom-bot-update-%d",time.Now().UnixNano())
 err=runChecked("systemd-run","--unit="+nameUnit,"--on-active=3s","/bin/sh",watchdogPath,target,backup,expected)
 if err!=nil{
  os.Remove(watchdogPath)
  _=writeAtomically(target,old,0755)
  return "",fmt.Errorf("Unable to launch external recovery task; Bot binary rolled back: %w",err)
 }
 return "New Bot binary installed. An independent systemd task will restart it and perform two health checks; failures restore the previous binary.",nil
}

func extractCoreExecutable(archive []byte) ([]byte,error) {
 zr,err:=zip.NewReader(bytes.NewReader(archive),int64(len(archive)));if err!=nil{return nil,err}
 for _,f:=range zr.File {
  if f.Name!="xray"{continue}
  if f.UncompressedSize64>maxUpdateBytes{return nil,errors.New("Xray executable exceeds the size limit")}
  rc,err:=f.Open();if err!=nil{return nil,err}
  out,err:=io.ReadAll(io.LimitReader(rc,maxUpdateBytes+1))
  rc.Close()
  if err!=nil{return nil,err}
  if len(out)<4 || !bytes.Equal(out[:4],[]byte{0x7f,'E','L','F'}){return nil,errors.New("Not a valid Xray ELF executable")}
  return out,nil
 }
 return nil,errors.New("Release archive does not contain an Xray executable")
}

func checkedCoreUpdate(ctx context.Context,target,configPath,statsServer,tag string) (string,error) {
 _,arch,err:=archNames();if err!=nil{return "",err}
 if !safeTag.MatchString(tag){return "",errors.New("Invalid version tag")}
 name:="Xray-linux-"+arch+".zip"
 base:="https://github.com/"+coreRepo+"/releases/download/"+tag+"/"+name
 archive,err:=downloadVerifiedHTTPS(ctx,base,maxUpdateBytes);if err!=nil{return "",err}
 dgst,err:=downloadVerifiedHTTPS(ctx,base+".dgst",8<<10);if err!=nil{return "",err}
 if err=verifiedDigest(archive,dgst,name);err!=nil{return "",err}
 executable,err:=extractCoreExecutable(archive);if err!=nil{return "",err}
 lock,err:=acquireConfigLock(configPath,20*time.Second);if err!=nil{return "",err}
 defer releaseConfigLock(lock)
 previous,err:=os.ReadFile(target);if err!=nil{return "",err}
 tmp,err:=os.CreateTemp(filepath.Dir(target),".xray-check-*");if err!=nil{return "",err}
 defer os.Remove(tmp.Name())
 if err=tmp.Chmod(0755);err!=nil{tmp.Close();return "",err}
 if _,err=tmp.Write(executable);err!=nil{tmp.Close();return "",err}
 if err=tmp.Close();err!=nil{return "",err}
 if err=runChecked(tmp.Name(),"run","-test","-config",configPath);err!=nil{return "",fmt.Errorf("New core is incompatible with the current config; upgrade cancelled: %w",err)}
 if bytes.Equal(previous,executable){return "Xray is already at this version",nil}
 backup:=target+".previous"
 if err=writeAtomically(backup,previous,0755);err!=nil{return "",err}
 snapshotTrafficBestEffortLocked(configPath,target,statsServer)
 if err=writeAtomically(target,executable,0755);err!=nil{return "",err}
 if err=runChecked("systemctl","restart","xray");err==nil {err=runChecked("systemctl","is-active","--quiet","xray")}
 if err!=nil {
  binaryRestoreErr:=writeAtomically(target,previous,0755)
  restoreErr:=runChecked("systemctl","restart","xray")
  return "",fmt.Errorf("New core failed to start and rollback was attempted (binary restore: %v; service restore: %v): %w",binaryRestoreErr,restoreErr,err)
 }
 return "Updated Xray-core to "+tag+"; previous binary: "+backup+" (verify a real client handshake)",nil
}


func checkedCoreRollback(target,configPath,statsServer string) (string,error) {
 lock,err:=acquireConfigLock(configPath,20*time.Second);if err!=nil{return "",err}
 defer releaseConfigLock(lock)
 previousPath:=target+".previous"
 old,err:=os.ReadFile(previousPath);if err!=nil{return "",fmt.Errorf("No previous core backup is available: %w",err)}
 current,err:=os.ReadFile(target);if err!=nil{return "",err}
 tmp,err:=os.CreateTemp(filepath.Dir(target),".xray-rollback-*");if err!=nil{return "",err}
 defer os.Remove(tmp.Name())
 if err=tmp.Chmod(0755);err!=nil{tmp.Close();return "",err}
 if _,err=tmp.Write(old);err!=nil{tmp.Close();return "",err}
 if err=tmp.Close();err!=nil{return "",err}
 if err=runChecked(tmp.Name(),"run","-test","-config",configPath);err!=nil{return "",fmt.Errorf("Previous core is incompatible with the current configuration: %w",err)}
 snapshotTrafficBestEffortLocked(configPath,target,statsServer)
 if err=writeAtomically(target,old,0755);err!=nil{return "",err}
 if err=runChecked("systemctl","restart","xray");err==nil{err=runChecked("systemctl","is-active","--quiet","xray")}
 if err!=nil {
  binaryRestoreErr:=writeAtomically(target,current,0755)
  restoreErr:=runChecked("systemctl","restart","xray")
  return "",fmt.Errorf("Rollback failed; attempted to restore the current core (binary restore: %v; service restore: %v): %w",binaryRestoreErr,restoreErr,err)
 }
 if err=writeAtomically(previousPath,current,0755);err!=nil{return "Core rolled back, but could not update the previous-version backup: "+err.Error(),nil}
 return "Previous Xray-core version restored; perform rollback again to return to the newer version.",nil
}
