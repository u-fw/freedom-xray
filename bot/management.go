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
   if c["email"]==name {
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

func commitManagedConfig(configPath,xrayBinary string,apply func(map[string]any)(string,error)) (string,error) {
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
 if bytes.Equal(original,next) {return "",errors.New("No configuration changes")}
 // Validate a staged candidate before touching the live path.
 candidate,err:=os.CreateTemp(filepath.Dir(configPath),".freedom-check-*.json");if err!=nil{return "",err}
 candidatePath:=candidate.Name();defer os.Remove(candidatePath)
 if err=candidate.Chmod(stat.Mode().Perm());err!=nil{candidate.Close();return "",err}
 if _,err=candidate.Write(next);err!=nil{candidate.Close();return "",err}
 if err=candidate.Close();err!=nil{return "",err}
 if err=runChecked(xrayBinary,"run","-test","-config",candidatePath);err!=nil{return "",fmt.Errorf("Xray configuration validation failed: %w",err)}
 backup:=fmt.Sprintf("%s.bot.bak.%s",configPath,time.Now().Format("20060102T150405.000000000"))
 if err=os.WriteFile(backup,original,0600);err!=nil{return "",err}
 if err=writeAtomically(configPath,next,stat.Mode().Perm());err!=nil{return "",err}
 snapshotTrafficBestEffortLocked(configPath,xrayBinary,"127.0.0.1:10085")
 if err=runChecked("systemctl","restart","xray");err==nil {
  err=runChecked("systemctl","is-active","--quiet","xray")
 }
 if err!=nil {
  failed:=err
  if restoreErr:=writeAtomically(configPath,original,stat.Mode().Perm());restoreErr!=nil {
   return "",fmt.Errorf("Xray restart failed (%v); restoring config failed (%v); backup: %s",failed,restoreErr,backup)
  }
  restartErr:=runChecked("systemctl","restart","xray")
  if restartErr!=nil {return "",fmt.Errorf("Original configuration restored, but Xray restart failed: %v; original error: %v",restartErr,failed)}
  return "",fmt.Errorf("New configuration failed startup verification and was rolled back: %w",failed)
 }
 return msg,nil
}
