package main

import (
 "bytes"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "sort"
 "syscall"
 "time"
)

type suspendedAccount struct {
 UUID string `json:"uuid"`
 Nodes map[string]map[string]any `json:"nodes"`
}

type suspendedStore struct {
 Users map[string]suspendedAccount `json:"users"`
}

func suspendedStorePath(configPath string) string {
 return filepath.Join(filepath.Dir(configPath),"freedom-disabled-users.json")
}

func readSuspendedStore(configPath string) (suspendedStore,[]byte,error) {
 path:=suspendedStorePath(configPath)
 s:=suspendedStore{Users:map[string]suspendedAccount{}}
 data,err:=os.ReadFile(path)
 if os.IsNotExist(err){return s,nil,nil}
 if err!=nil{return s,nil,err}
 if err=json.Unmarshal(data,&s);err!=nil{return s,nil,fmt.Errorf("停用用户记录损坏: %w",err)}
 if s.Users==nil{s.Users=map[string]suspendedAccount{}}
 return s,data,nil
}

func disabledUserNames(configPath string) ([]string,error) {
 store,_,err:=readSuspendedStore(configPath);if err!=nil{return nil,err}
 names:=make([]string,0,len(store.Users))
 for name:=range store.Users{names=append(names,name)}
 sort.Strings(names)
 return names,nil
}

func applyUserSuspension(root map[string]any,store *suspendedStore,change UserChange) (string,error) {
 if !managedUserName.MatchString(change.User){return "",errors.New("用户标识格式不正确")}
 inbounds,err:=usableInbounds(root);if err!=nil{return "",err}
 if store.Users==nil{store.Users=map[string]suspendedAccount{}}
 if change.Kind=="disable" {
  if _,exists:=store.Users[change.User];exists{return "",errors.New("该用户已经停用")}
  ids,err:=managedUserIDs(inbounds,change.User);if err!=nil{return "",err}
  if len(ids)!=1{return "",errors.New("用户不存在或各节点 UUID 不一致，拒绝停用")}
  var uuid string;for id:=range ids{uuid=id}
  parked:=suspendedAccount{UUID:uuid,Nodes:map[string]map[string]any{}}
  // Validate every affected inbound before changing any of them.
  for _,in:=range inbounds {
   list,_:=clientList(in)
   matches:=0
   for _,v:=range list{c,_:=getMap(v);if c["email"]==change.User {matches++;if c["id"]!=uuid{return "",errors.New("UUID 冲突")};parked.Nodes[in["tag"].(string)]=c}}
   if matches>1{return "",fmt.Errorf("节点 %v 中用户重复",in["tag"])}
   if matches>0 && len(list)<=1{return "",fmt.Errorf("节点 %v 只有此用户，拒绝停用最后一个客户端",in["tag"])}
  }
  if len(parked.Nodes)==0{return "",errors.New("未找到用户")}
  for _,in:=range inbounds {
   settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   next:=make([]any,0,len(list))
   for _,v:=range list{c,_:=getMap(v);if c["email"]!=change.User {next=append(next,v)}}
   settings["clients"]=next
  }
  store.Users[change.User]=parked
  return fmt.Sprintf("已停用 %s（%d 个入站），原 UUID 已安全保存在本地",change.User,len(parked.Nodes)),nil
 }
 if change.Kind=="enable" {
  parked,exists:=store.Users[change.User];if !exists{return "",errors.New("没有该用户的停用记录")}
  if parked.UUID==""||len(parked.Nodes)==0{return "",errors.New("停用记录缺少 UUID 或原节点")}
  ids,err:=managedUserIDs(inbounds,change.User);if err!=nil{return "",err}
  if len(ids)>0{return "",errors.New("该名称已有活跃用户，拒绝恢复")}
  byTag:=make(map[string]map[string]any)
  for _,in:=range inbounds{tag,ok:=in["tag"].(string);if ok{byTag[tag]=in}}
  for tag,client:=range parked.Nodes {
   in,ok:=byTag[tag];if !ok{return "",fmt.Errorf("原节点 %s 已删除，拒绝部分恢复",tag)}
   if client["email"]!=change.User||client["id"]!=parked.UUID{return "",errors.New("停用记录身份不匹配")}
   list,_:=clientList(in)
   for _,v:=range list{
    c,_:=getMap(v)
    if c["id"]==parked.UUID {return "",fmt.Errorf("节点 %s 上 UUID 已被使用",tag)}
   }
  }
  for tag,client:=range parked.Nodes{
   in:=byTag[tag];settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   settings["clients"]=append(list,client)
  }
  delete(store.Users,change.User)
  return fmt.Sprintf("已恢复 %s 的 %d 个入站和原 UUID",change.User,len(parked.Nodes)),nil
 }
 return "",errors.New("未知启停操作")
}

// Both files are backed up and restored together if a write or restart fails.
// The shared file lock prevents simultaneous Telegram changes.
func changeUserSuspension(configPath,xrayBinary string,change UserChange) (string,error) {
 lock,err:=os.OpenFile(filepath.Join(filepath.Dir(configPath),".freedom-config.lock"),os.O_CREATE|os.O_RDWR,0600)
 if err!=nil{return "",err}
 defer lock.Close()
 if err=syscall.Flock(int(lock.Fd()),syscall.LOCK_EX);err!=nil{return "",err}
 defer syscall.Flock(int(lock.Fd()),syscall.LOCK_UN)
 original,err:=os.ReadFile(configPath);if err!=nil{return "",err}
 st,err:=os.Stat(configPath);if err!=nil{return "",err}
 store,oldState,err:=readSuspendedStore(configPath);if err!=nil{return "",err}
 var root map[string]any
 if err=json.Unmarshal(original,&root);err!=nil{return "",err}
 message,err:=applyUserSuspension(root,&store,change);if err!=nil{return "",err}
 next,err:=json.MarshalIndent(root,"","  ");if err!=nil{return "",err}
 next=append(next,'
')
 newState,err:=json.MarshalIndent(store,"","  ");if err!=nil{return "",err}
 newState=append(newState,'
')
 // Check a staged config before modifying either original.
 check,err:=os.CreateTemp(filepath.Dir(configPath),".freedom-check-*.json");if err!=nil{return "",err}
 defer os.Remove(check.Name())
 if err=check.Chmod(st.Mode().Perm());err!=nil{check.Close();return "",err}
 if _,err=check.Write(next);err!=nil{check.Close();return "",err}
 if err=check.Close();err!=nil{return "",err}
 if err=runChecked(xrayBinary,"run","-test","-config",check.Name());err!=nil{return "",fmt.Errorf("Xray 配置测试失败: %w",err)}
 version:=time.Now().Format("20060102T150405.000000000")
 if err=os.WriteFile(configPath+".bot.bak."+version,original,0600);err!=nil{return "",err}
 statePath:=suspendedStorePath(configPath)
 if oldState!=nil {
  if err=os.WriteFile(statePath+".bak."+version,oldState,0600);err!=nil{return "",err}
 }
 restore:=func() error {
  cfgErr:=writeAtomically(configPath,original,st.Mode().Perm())
  var stateErr error
  if oldState==nil {stateErr=os.Remove(statePath);if os.IsNotExist(stateErr){stateErr=nil}}else{stateErr=writeAtomically(statePath,oldState,0600)}
  if cfgErr!=nil || stateErr!=nil{return fmt.Errorf("恢复文件失败: config=%v, store=%v",cfgErr,stateErr)}
  return nil
 }
 if err=writeAtomically(statePath,newState,0600);err!=nil{
  if oldState==nil{_ = os.Remove(statePath)}
  return "",err
 }
 if err=writeAtomically(configPath,next,st.Mode().Perm());err!=nil{
  if restoreErr:=restore();restoreErr!=nil{return "",fmt.Errorf("写配置失败: %w；回滚失败: %v",err,restoreErr)}
  return "",err
 }
 if err=runChecked("systemctl","restart","xray");err==nil{err=runChecked("systemctl","is-active","--quiet","xray")}
 if err!=nil {
  failure:=err
  if restoreErr:=restore();restoreErr!=nil{return "",fmt.Errorf("Xray 启动失败: %v，且回滚失败: %v",failure,restoreErr)}
  restartErr:=runChecked("systemctl","restart","xray")
  return "",fmt.Errorf("操作失败，已恢复原始配置并尝试启动: %v (恢复服务: %v)",failure,restartErr)
 }
 return message,nil
}

// Keep the existing management interface and route only suspend/resume to
// the two-file transaction. No client UUID is exposed to untrusted callers.
func changeManagedUser(configPath,xrayBinary string,change UserChange)(string,error){
 if change.Kind=="disable"||change.Kind=="enable"{return changeUserSuspension(configPath,xrayBinary,change)}
 if change.Kind=="add" {
  store,_,err:=readSuspendedStore(configPath);if err!=nil{return "",err}
  if _,exists:=store.Users[change.User];exists{return "",errors.New("此名称存在停用用户，请先恢复或使用不同的名称")}
 }
 return ChangeUserConfig(configPath,xrayBinary,change)
}

// Prevent stale disabled accounts from being mistaken for active accounts
// when rendering Telegram summaries.
func isSuspendedUser(configPath,name string) bool {
 store,_,err:=readSuspendedStore(configPath)
 if err!=nil{return false}
 _,ok:=store.Users[name]
 return ok
}

var _ = bytes.Equal
