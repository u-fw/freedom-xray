package main

import (
 "bytes"
 "crypto/rand"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "os/exec"
 "path/filepath"
 "regexp"
 "strings"
 "syscall"
 "time"
)

var managedUserName = regexp.MustCompile("^[A-Za-z0-9_.@-]{1,48}$")

// errNoConfigChanges lets multi-file transactions distinguish a benign no-op
// from a real failure without matching error strings.
var errNoConfigChanges = errors.New("No configuration changes")

// UserChange is a restricted, declarative operation. No user input reaches a shell.
type UserChange struct {
 Kind string
 User string
 NewName string
 Node string
}

func newManagedUUID() (string, error) {
 var b [16]byte
 if _, err := rand.Read(b[:]); err != nil { return "", err }
 b[6] = (b[6] & 0x0f) | 0x40
 b[8] = (b[8] & 0x3f) | 0x80
 h := hex.EncodeToString(b[:])
 return fmt.Sprintf("%s-%s-%s-%s-%s",h[:8],h[8:12],h[12:16],h[16:20],h[20:]),nil
}

func getMap(v any) (map[string]any,bool) {
 m,ok:=v.(map[string]any); return m,ok
}

func clientList(in map[string]any) ([]any,error) {
 settings,ok:=getMap(in["settings"]); if !ok{return nil,errors.New("Inbound lacks settings")}
 clients,ok:=settings["clients"].([]any); if !ok {return nil,errors.New("Inbound lacks clients")}
 return clients,nil
}

func usableInbounds(root map[string]any) ([]map[string]any,error) {
 raw,ok:=root["inbounds"].([]any); if !ok {return nil,errors.New("Missing inbounds")}
 out:=make([]map[string]any,0,len(raw))
 for _,value:=range raw {
  in,ok:=getMap(value); if !ok {return nil,errors.New("Invalid inbound format")}
  if in["tag"]=="api-internal" {continue}
  if in["protocol"]!="vless" {continue}
  if _,err:=clientList(in);err!=nil{return nil,err}
  out=append(out,in)
 }
 return out,nil
}

func managedUserIDs(inbounds []map[string]any,name string) (map[string]bool,error) {
 ids:=make(map[string]bool)
 for _,in:=range inbounds {
  list,_:=clientList(in)
  for _,v:=range list {
   c,ok:=getMap(v);if !ok {return nil,errors.New("Invalid client format")}
   email,_:=c["email"].(string)
   if bindingOwner(email)==name {
    id,_:=c["id"].(string);if id=="" {return nil,errors.New("User UUID is empty")}
    ids[id]=true
   }
  }
 }
 return ids,nil
}

func applyManagedUserChange(root map[string]any,change UserChange) (string,error) {
 dir,err:=directoryFromConfig(root);if err!=nil{return "",err}
 return applyDirectoryUserChange(root,&dir,change)
}

func runChecked(command string,args ...string) error {
 c:=exec.Command(command,args...)
 c.Env=os.Environ()
 out,err:=c.CombinedOutput()
 if err!=nil {return fmt.Errorf("%s: %w: %s",command,err,strings.TrimSpace(string(out)))}
 return nil
}

// writeAtomically keeps the previous file untouched until the replacement is ready.
func writeAtomically(path string,data []byte,perm os.FileMode) error {
 f,err:=os.CreateTemp(filepath.Dir(path),".freedom-write-*");if err!=nil{return err}
 name:=f.Name()
 defer os.Remove(name)
 if original,statErr:=os.Stat(path);statErr==nil {
  if st,ok:=original.Sys().(*syscall.Stat_t);ok { if err=f.Chown(int(st.Uid),int(st.Gid));err!=nil {f.Close();return err} }
 }
 if err=f.Chmod(perm);err!=nil{f.Close();return err}
 if _,err=f.Write(data);err!=nil{f.Close();return err}
 if err=f.Sync();err!=nil{f.Close();return err}
 if err=f.Close();err!=nil{return err}
 return os.Rename(name,path)
}

// ChangeUserConfig serializes local Bot changes, validates before restart,
// writes an owner-readable backup and rolls back if restart or health check fails.
func ChangeUserConfig(configPath,xrayBinary string,change UserChange) (string,error) {
 return changeDirectoryUser(configPath,xrayBinary,change)
}

// configSidecar is an auxiliary file committed together with config.json.
// Transform runs under the configuration lock and receives the file's current
// bytes (and whether it existed), so a sidecar update cannot race a peer.
// Returning nil removes the file. BeforeValidation is used for files Xray
// itself must see during `xray run -test`, such as new TLS certificates.
type configSidecar struct {
 Path string
 Mode os.FileMode
 BeforeValidation bool
 Transform func(original []byte,exists bool) ([]byte,error)
}

type stagedSidecar struct {
 path string
 mode os.FileMode
 original []byte
 hadOriginal bool
 next []byte
 changed bool
 beforeValidation bool
 written bool
}

func stageConfigSidecars(sidecars []configSidecar) ([]*stagedSidecar,error) {
 seen:=map[string]bool{}
 staged:=make([]*stagedSidecar,0,len(sidecars))
 for _,sidecar:=range sidecars {
  path:=strings.TrimSpace(sidecar.Path)
  if path==""||sidecar.Transform==nil{return nil,errors.New("Invalid sidecar transaction")}
  if seen[path]{return nil,fmt.Errorf("Duplicate sidecar path %s",path)}
  seen[path]=true
  original,err:=os.ReadFile(path)
  hadOriginal:=true
  if os.IsNotExist(err){original=nil;hadOriginal=false}else if err!=nil{return nil,err}
  mode:=sidecar.Mode
  if mode==0 {
   mode=0600
   if hadOriginal {
    if info,statErr:=os.Stat(path);statErr==nil{mode=info.Mode().Perm()}
   }
  }
  next,err:=sidecar.Transform(original,hadOriginal)
  if err!=nil{return nil,err}
  changed:=next!=nil&&(!hadOriginal||!bytes.Equal(original,next))
  if next==nil&&hadOriginal{changed=true}
  staged=append(staged,&stagedSidecar{
   path:path,mode:mode,original:original,hadOriginal:hadOriginal,
   next:next,changed:changed,beforeValidation:sidecar.BeforeValidation,
  })
 }
 return staged,nil
}

func writeSidecar(s *stagedSidecar) error {
 if s.next==nil {
  if err:=os.Remove(s.path);err!=nil&&!os.IsNotExist(err){return err}
  s.written=true
  return nil
 }
 if err:=os.MkdirAll(filepath.Dir(s.path),0755);err!=nil{return err}
 if err:=writeAtomically(s.path,s.next,s.mode);err!=nil{return err}
 s.written=true
 return nil
}

func restoreSidecar(s *stagedSidecar) error {
 if s.hadOriginal{return writeAtomically(s.path,s.original,s.mode)}
 if err:=os.Remove(s.path);err!=nil&&!os.IsNotExist(err){return err}
 return nil
}

// commitManagedConfigTx applies config.json and any sidecars as one
// transaction: lock, decode, mutate, stage, validate, back up, write, restart,
// and restore every file if any step fails.
func commitManagedConfigTx(configPath,xrayBinary,statsServer string,sidecars []configSidecar,apply func(map[string]any)(string,error)) (string,error) {
 if strings.TrimSpace(statsServer)==""{statsServer="127.0.0.1:10085"}
 lock,err:=acquireConfigLock(configPath,20*time.Second);if err!=nil{return "",err}
 defer releaseConfigLock(lock)
 original,err:=os.ReadFile(configPath);if err!=nil{return "",err}
 stat,err:=os.Stat(configPath);if err!=nil{return "",err}
 var root map[string]any
 decoder:=json.NewDecoder(bytes.NewReader(original));decoder.UseNumber()
 if err=decoder.Decode(&root);err!=nil{return "",err}
 msg,err:=apply(root);if err!=nil{return "",err}
 next,err:=json.MarshalIndent(root,"","  ");if err!=nil{return "",err}
 next=append(next,'\n')
 staged,err:=stageConfigSidecars(sidecars);if err!=nil{return "",err}
 sidecarsChanged:=false
 for _,s:=range staged{if s.changed{sidecarsChanged=true}}
 if bytes.Equal(original,next)&&!sidecarsChanged{return "",errNoConfigChanges}

	// Certificates that validation depends on must exist first; roll them back
	// if the staged config is rejected.
	for _,s:=range staged {
		if !s.changed||!s.beforeValidation{continue}
		if err:=writeSidecar(s);err!=nil{
			rollbackSidecars(staged)
			return "",err
		}
	}

 // Validate a staged candidate before touching the live path.
 candidate,err:=os.CreateTemp(filepath.Dir(configPath),".freedom-check-*.json");if err!=nil{
  rollbackSidecars(staged);return "",err
 }
 candidatePath:=candidate.Name();defer os.Remove(candidatePath)
 if err=candidate.Chmod(stat.Mode().Perm());err!=nil{candidate.Close();rollbackSidecars(staged);return "",err}
 if _,err=candidate.Write(next);err!=nil{candidate.Close();rollbackSidecars(staged);return "",err}
 if err=candidate.Close();err!=nil{rollbackSidecars(staged);return "",err}
 if err=runChecked(xrayBinary,"run","-test","-config",candidatePath);err!=nil{
  rollbackSidecars(staged)
  return "",fmt.Errorf("Xray configuration validation failed: %w",err)
 }

 stamp:=time.Now().Format("20060102T150405.000000000")
 backup:=fmt.Sprintf("%s.bot.bak.%s",configPath,stamp)
 if err=os.WriteFile(backup,original,0600);err!=nil{rollbackSidecars(staged);return "",err}
 for _,s:=range staged {
  if !s.changed||!s.hadOriginal{continue}
  if err=os.WriteFile(fmt.Sprintf("%s.bot.bak.%s",s.path,stamp),s.original,0600);err!=nil{
   rollbackSidecars(staged);return "",err
  }
 }

 for _,s:=range staged {
  if !s.changed||s.written{continue}
  if err=writeSidecar(s);err!=nil{
   rollbackConfigAndSidecars(configPath,original,stat,staged)
   return "",err
  }
 }
 if err=writeAtomically(configPath,next,stat.Mode().Perm());err!=nil{
  rollbackConfigAndSidecars(configPath,original,stat,staged)
  return "",err
 }
 snapshotTrafficBestEffortLocked(configPath,xrayBinary,statsServer)
 if err=runChecked("systemctl","restart","xray");err==nil {
  err=runChecked("systemctl","is-active","--quiet","xray")
 }
 if err!=nil {
  failed:=err
  restoreErr:=rollbackConfigAndSidecars(configPath,original,stat,staged)
  if restoreErr!=nil {
   return "",fmt.Errorf("Xray restart failed (%v); restoring files failed (%v); backup: %s",failed,restoreErr,backup)
  }
  restartErr:=runChecked("systemctl","restart","xray")
  if restartErr!=nil {return "",fmt.Errorf("Original configuration restored, but Xray restart failed: %v; original error: %v",restartErr,failed)}
  return "",fmt.Errorf("New configuration failed startup verification and was rolled back: %w",failed)
 }
 return msg,nil
}

func rollbackSidecars(staged []*stagedSidecar) error {
 var failures []string
 for _,s:=range staged {
  if !s.written{continue}
  if err:=restoreSidecar(s);err!=nil{failures=append(failures,fmt.Sprintf("%s: %v",s.path,err))}
 }
 if len(failures)>0{return errors.New(strings.Join(failures,"; "))}
 return nil
}

func rollbackConfigAndSidecars(configPath string,original []byte,stat os.FileInfo,staged []*stagedSidecar) error {
 var failures []string
 if err:=writeAtomically(configPath,original,stat.Mode().Perm());err!=nil{
  failures=append(failures,fmt.Sprintf("config: %v",err))
 }
 if err:=rollbackSidecars(staged);err!=nil{failures=append(failures,err.Error())}
 if len(failures)>0{return errors.New(strings.Join(failures,"; "))}
 return nil
}

func commitManagedConfig(configPath,xrayBinary,statsServer string,apply func(map[string]any)(string,error)) (string,error) {
 return commitManagedConfigTx(configPath,xrayBinary,statsServer,nil,apply)
}
