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
 settings,ok:=getMap(in["settings"]); if !ok{return nil,errors.New("入站缺少 settings")}
 clients,ok:=settings["clients"].([]any); if !ok {return nil,errors.New("入站缺少 clients")}
 return clients,nil
}

func usableInbounds(root map[string]any) ([]map[string]any,error) {
 raw,ok:=root["inbounds"].([]any); if !ok {return nil,errors.New("缺少 inbounds")}
 out:=make([]map[string]any,0,len(raw))
 for _,value:=range raw {
  in,ok:=getMap(value); if !ok {return nil,errors.New("入站格式无效")}
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
   c,ok:=getMap(v);if !ok {return nil,errors.New("客户端格式无效")}
   if c["email"]==name {
    id,_:=c["id"].(string);if id=="" {return nil,errors.New("用户 UUID 为空")}
    ids[id]=true
   }
  }
 }
 return ids,nil
}

func applyManagedUserChange(root map[string]any,change UserChange) (string,error) {
 if !managedUserName.MatchString(change.User) {return "",errors.New("用户名称必须为 1–48 位英文字母、数字、_、.、@ 或 -")}
 inbounds,err:=usableInbounds(root);if err!=nil{return "",err}
 ids,err:=managedUserIDs(inbounds,change.User);if err!=nil{return "",err}
 if len(ids)>1{return "",errors.New("同名用户在不同节点使用多个 UUID，拒绝批量修改，请先在本机统一")}
 exists:=len(ids)==1
 var oldID string;for k:=range ids {oldID=k}
 switch change.Kind {
 case "add":
  if exists{return "",errors.New("用户已存在")}
  if len(inbounds)==0{return "",errors.New("不存在可用的 VLESS 入站")}
  uuid,err:=newManagedUUID();if err!=nil{return "",err}
  selected:=0
  for _,in:=range inbounds {
   tag,_:=in["tag"].(string)
   if change.Node!="" && change.Node!="all" && change.Node!=tag {continue}
   settings,_:=getMap(in["settings"]); list,_:=clientList(in)
   client:=map[string]any{"id":uuid,"email":change.User}
   if len(list)>0 {if c,ok:=getMap(list[0]);ok {if flow,ok:=c["flow"].(string);ok && flow!="" {client["flow"]=flow}}}
   settings["clients"]=append(list,client);selected++
  }
  if selected==0 {return "",errors.New("指定节点不存在")}
  return "新增用户 "+change.User+"，UUID: "+uuid,nil
 case "rename":
  if !exists{return "",errors.New("找不到用户")}
  if !managedUserName.MatchString(change.NewName) {return "",errors.New("新名称格式无效")}
  newIDs,err:=managedUserIDs(inbounds,change.NewName);if err!=nil{return "",err}
  if len(newIDs)>0{return "",errors.New("新名称已存在")}
  for _,in:=range inbounds {list,_:=clientList(in);for _,v:=range list {c,_:=getMap(v);if c["email"]==change.User && c["id"]==oldID {c["email"]=change.NewName}}}
  return "用户已改名为 "+change.NewName,nil
 case "rotate":
  if !exists{return "",errors.New("找不到用户")}
  uuid,err:=newManagedUUID();if err!=nil{return "",err}
  for _,in:=range inbounds {list,_:=clientList(in);for _,v:=range list {c,_:=getMap(v);if c["email"]==change.User && c["id"]==oldID {c["id"]=uuid}}}
  return "UUID 已轮换，新 UUID: "+uuid,nil
 case "delete":
  if !exists{return "",errors.New("找不到用户")}
  for _,in:=range inbounds {
   settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   next:=make([]any,0,len(list))
   for _,v:=range list {c,_:=getMap(v);if c["email"]!=change.User {next=append(next,v)}}
   if len(next)==0{return "",fmt.Errorf("入站 %v 仅有此用户；为避免误断开全部连接，拒绝删除",in["tag"])}
   settings["clients"]=next
  }
  return "已从所有关联入站删除用户 "+change.User,nil
 case "assign":
  if !exists{return "",errors.New("用户不存在，请先新增")}
  for _,in:=range inbounds {
   if in["tag"]!=change.Node {continue}
   settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   for _,v:=range list {c,_:=getMap(v);if c["email"]==change.User {return "",errors.New("用户已分配到该节点")}}
   nc:=map[string]any{"id":oldID,"email":change.User}
   if len(list)>0 {if first,ok:=getMap(list[0]);ok {if f,ok:=first["flow"].(string);ok && f!="" {nc["flow"]=f}}}
   settings["clients"]=append(list,nc)
   return "已分配 "+change.User+" 到 "+change.Node,nil
  }
  return "",errors.New("目标入站不存在")
 case "unassign":
  if !exists{return "",errors.New("用户不存在")}
  for _,in:=range inbounds {
   if in["tag"]!=change.Node {continue}
   settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   next:=make([]any,0,len(list));found:=false
   for _,v:=range list {c,_:=getMap(v);if c["email"]==change.User && c["id"]==oldID {found=true}else{next=append(next,v)}}
   if !found{return "",errors.New("用户未分配到该节点")}
   if len(next)==0{return "",errors.New("无法移除节点的最后一位用户")}
   settings["clients"]=next
   return "已从 "+change.Node+" 移除 "+change.User,nil
  }
  return "",errors.New("目标入站不存在")
 }
 return "",errors.New("未知操作")
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
 return commitManagedConfig(configPath,xrayBinary,func(root map[string]any)(string,error){return applyManagedUserChange(root,change)})
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
 if bytes.Equal(original,next) {return "",errors.New("配置未发生变化")}
 // Validate a staged candidate before touching the live path.
 candidate,err:=os.CreateTemp(filepath.Dir(configPath),".freedom-check-*.json");if err!=nil{return "",err}
 candidatePath:=candidate.Name();defer os.Remove(candidatePath)
 if err=candidate.Chmod(stat.Mode().Perm());err!=nil{candidate.Close();return "",err}
 if _,err=candidate.Write(next);err!=nil{candidate.Close();return "",err}
 if err=candidate.Close();err!=nil{return "",err}
 if err=runChecked(xrayBinary,"run","-test","-config",candidatePath);err!=nil{return "",fmt.Errorf("Xray 配置校验失败: %w",err)}
 backup:=fmt.Sprintf("%s.bot.bak.%s",configPath,time.Now().Format("20060102T150405.000000000"))
 if err=os.WriteFile(backup,original,0600);err!=nil{return "",err}
 if err=writeAtomically(configPath,next,stat.Mode().Perm());err!=nil{return "",err}
 if err=runChecked("systemctl","restart","xray");err==nil {
  err=runChecked("systemctl","is-active","--quiet","xray")
 }
 if err!=nil {
  failed:=err
  if restoreErr:=writeAtomically(configPath,original,stat.Mode().Perm());restoreErr!=nil {
   return "",fmt.Errorf("Xray 重启失败 (%v)，恢复配置失败 (%v)，备份: %s",failed,restoreErr,backup)
  }
  restartErr:=runChecked("systemctl","restart","xray")
  if restartErr!=nil {return "",fmt.Errorf("已恢复原配置，但 Xray 恢复启动失败: %v；原始错误: %v",restartErr,failed)}
  return "",fmt.Errorf("新配置未通过启动检测，已回滚: %w",failed)
 }
 return msg,nil
}
