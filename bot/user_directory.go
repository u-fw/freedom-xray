package main

import (
 "bytes"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "sort"
 "strings"
 "time"
)

type directoryUser struct {
 ID string `json:"id"`
}
type userDirectory struct {
 Users map[string]directoryUser `json:"users"`
}

func directoryPath(configPath string) string {
 return filepath.Join(filepath.Dir(configPath),"freedom-users.json")
}

// Existing inbounds remain the source of truth for legacy credentials; the
// sidecar provides persistence for users not bound to any inbound.
func directoryFromConfig(root map[string]any) (userDirectory,error) {
 directory:=userDirectory{Users:map[string]directoryUser{}}
 inbounds,err:=usableInbounds(root);if err!=nil{return directory,err}
 for _,in:=range inbounds {
  clients,_:=clientList(in)
  for _,v:=range clients {
   c,ok:=getMap(v);if !ok{return directory,errors.New("用户配置无效")}
   email,_:=c["email"].(string);id,_:=c["id"].(string)
   if email==""||id==""{continue}
   if saved,ok:=directory.Users[email];ok&&saved.ID!=id{
    return directory,fmt.Errorf("用户 %s 在不同入站的 UUID 不一致，请先本机修复",email)
   }
   directory.Users[email]=directoryUser{ID:id}
  }
 }
 return directory,nil
}
func loadUserDirectory(configPath string,root map[string]any)(userDirectory,[]byte,error){
 merged,err:=directoryFromConfig(root);if err!=nil{return merged,nil,err}
 old,err:=os.ReadFile(directoryPath(configPath))
 if os.IsNotExist(err){return merged,nil,nil}
 if err!=nil{return merged,nil,err}
 var saved userDirectory
 if err=json.Unmarshal(old,&saved);err!=nil{return merged,nil,fmt.Errorf("用户目录损坏: %w",err)}
 for name,u:=range saved.Users {
  if !managedUserName.MatchString(name)||u.ID==""{return merged,nil,errors.New("用户目录包含非法名称或空 UUID")}
  if existing,ok:=merged.Users[name];ok&&existing.ID!=u.ID {
   return merged,nil,fmt.Errorf("用户 %s 的目录 UUID 与 Xray 配置不一致",name)
  }
  merged.Users[name]=u
 }
 return merged,old,nil
}

func allDirectoryNames(configPath string)([]string,error){
 config,err:=os.ReadFile(configPath);if err!=nil{return nil,err}
 var root map[string]any
 if err=json.Unmarshal(config,&root);err!=nil{return nil,err}
 dir,_,err:=loadUserDirectory(configPath,root);if err!=nil{return nil,err}
 names:=make([]string,0,len(dir.Users))
 for name:=range dir.Users{names=append(names,name)}
 sort.Strings(names)
 return names,nil
}

func userFlowForInbound(in map[string]any) string {
 stream,ok:=getMap(in["streamSettings"]);if !ok{return ""}
 network,_:=stream["network"].(string)
 security,_:=stream["security"].(string)
 settings,_:=getMap(in["settings"])
 vdec,_:=settings["decryption"].(string)
 allowed:=(network=="tcp"||network=="raw")&&(security=="tls"||security=="reality")||
  vdec!=""&&vdec!="none"
 if !allowed{return ""}
 clients,_:=clientList(in)
 for _,c:=range clients {
  client,_:=getMap(c)
  if flow,ok:=client["flow"].(string);ok&&flow=="xtls-rprx-vision"{return flow}
 }
 // Existing users without Vision should not unexpectedly acquire it when a
 // new account is added. For an empty inbound use transport policy defaults.
 if len(clients)>0{return ""}
 return "xtls-rprx-vision"
}

func applyDirectoryUserChange(root map[string]any,dir *userDirectory,change UserChange)(string,error){
 if !managedUserName.MatchString(change.User){return "",errors.New("用户名只能由 1–48 位字母、数字、_、.、@ 或 - 组成")}
 if dir.Users==nil{dir.Users=map[string]directoryUser{}}
 inbounds,err:=usableInbounds(root);if err!=nil{return "",err}
 existing,exists:=dir.Users[change.User]
 switch change.Kind {
 case "add":
  if exists{return "",errors.New("用户已存在")}
  uuid,err:=newManagedUUID();if err!=nil{return "",err}
  if change.Node!=""&&change.Node!="all" {
   found:=false
   for _,in:=range inbounds {if in["tag"]==change.Node{found=true}}
   if !found{return "",errors.New("指定节点不存在")}
  }
  dir.Users[change.User]=directoryUser{ID:uuid}
  if change.Node==""{return "已创建独立用户 "+change.User+"；暂未绑定任何节点，UUID: "+uuid,nil}
  affected:=0
  for _,in:=range inbounds{
   if change.Node!="all"&&in["tag"]!=change.Node{continue}
   settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   client:=map[string]any{"id":uuid,"email":change.User}
   if flow:=userFlowForInbound(in);flow!=""{client["flow"]=flow}
   settings["clients"]=append(list,client);affected++
  }
  if affected==0{return "",errors.New("没有可分配的节点")}
  return fmt.Sprintf("已创建用户 %s 并绑定 %d 个节点。UUID: %s",change.User,affected,uuid),nil
 case "rename":
  if !exists{return "",errors.New("用户不存在")}
  if !managedUserName.MatchString(change.NewName){return "",errors.New("新用户名不合法")}
  if _,ok:=dir.Users[change.NewName];ok{return "",errors.New("新名称已被占用")}
  for _,in:=range inbounds{list,_:=clientList(in);for _,value:=range list{
   c,_:=getMap(value);if c["email"]==change.User&&c["id"]==existing.ID{c["email"]=change.NewName}
  }}
  dir.Users[change.NewName]=existing;delete(dir.Users,change.User)
  return "用户已改名为 "+change.NewName,nil
 case "rotate":
  if !exists{return "",errors.New("用户不存在")}
  uuid,err:=newManagedUUID();if err!=nil{return "",err}
  for _,in:=range inbounds{list,_:=clientList(in);for _,value:=range list{
   c,_:=getMap(value);if c["email"]==change.User&&c["id"]==existing.ID{c["id"]=uuid}
  }}
  dir.Users[change.User]=directoryUser{ID:uuid}
  return "用户 "+change.User+" 的 UUID 已轮换为 "+uuid,nil
 case "delete":
  if !exists{return "",errors.New("用户不存在")}
  for _,in:=range inbounds {
   settings,_:=getMap(in["settings"]);list,_:=clientList(in);next:=make([]any,0,len(list))
   for _,value:=range list{
    c,_:=getMap(value)
    if c["email"]!=change.User{next=append(next,value)}
   }
   settings["clients"]=next
  }
  delete(dir.Users,change.User)
  return "已删除独立用户 "+change.User+" 及全部入站绑定",nil
 case "assign":
  if !exists{return "",errors.New("请先在用户管理创建用户")}
  for _,in:=range inbounds{
   if in["tag"]!=change.Node{continue}
   settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   for _,value:=range list{c,_:=getMap(value);if c["email"]==change.User{return "",errors.New("用户已绑定该节点")}}
   client:=map[string]any{"id":existing.ID,"email":change.User}
   if flow:=userFlowForInbound(in);flow!=""{client["flow"]=flow}
   settings["clients"]=append(list,client)
   return "已将 "+change.User+" 绑定到节点 "+change.Node,nil
  }
  return "",errors.New("节点不存在")
 case "unassign":
  if !exists{return "",errors.New("用户不存在")}
  for _,in:=range inbounds {
   if in["tag"]!=change.Node{continue}
   settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   next:=make([]any,0,len(list));found:=false
   for _,value:=range list {c,_:=getMap(value);if c["email"]==change.User&&c["id"]==existing.ID{found=true}else{next=append(next,value)}}
   if !found{return "",errors.New("用户尚未绑定此节点")}
   settings["clients"]=next
   return "已解除 "+change.User+" 与 "+change.Node+" 的绑定（用户身份仍保留）",nil
  }
  return "",errors.New("节点不存在")
 }
 return "",errors.New("不支持的用户操作")
}

func changeDirectoryUser(configPath,xrayBinary string,change UserChange)(string,error){
 lock,err:=acquireConfigLock(configPath,20*time.Second);if err!=nil{return "",err}
 defer releaseConfigLock(lock)
 original,err:=os.ReadFile(configPath);if err!=nil{return "",err}
 st,err:=os.Stat(configPath);if err!=nil{return "",err}
 var root map[string]any
 if err=json.Unmarshal(original,&root);err!=nil{return "",err}
 directory,oldDirectory,err:=loadUserDirectory(configPath,root);if err!=nil{return "",err}
 if change.Kind=="add" {
  disabled,_,err:=readSuspendedStore(configPath);if err!=nil{return "",err}
  if _,exists:=disabled.Users[change.User];exists{return "",errors.New("同名用户已被停用；请先恢复或使用其他名称")}
 }
 message,err:=applyDirectoryUserChange(root,&directory,change);if err!=nil{return "",err}
 nextConfig,err:=json.MarshalIndent(root,"","  ");if err!=nil{return "",err}
 nextConfig=append(nextConfig,10)
 nextDir,err:=json.MarshalIndent(directory,"","  ");if err!=nil{return "",err}
 nextDir=append(nextDir,10)
 configChanged:=!bytes.Equal(original,nextConfig)
 if configChanged {
  check,err:=os.CreateTemp(filepath.Dir(configPath),".freedom-user-check-*.json");if err!=nil{return "",err}
  defer os.Remove(check.Name())
  if err=check.Chmod(st.Mode().Perm());err!=nil{check.Close();return "",err}
  if _,err=check.Write(nextConfig);err!=nil{check.Close();return "",err}
  if err=check.Close();err!=nil{return "",err}
  if err=runChecked(xrayBinary,"run","-test","-config",check.Name());err!=nil{return "",fmt.Errorf("Xray 拒绝新配置（未提交）：%w",err)}
 }
 path:=directoryPath(configPath)
 stamp:=time.Now().Format("20060102T150405.000000000")
 if err=os.WriteFile(configPath+".bot.bak."+stamp,original,0600);err!=nil{return "",err}
 if oldDirectory!=nil {
  if err=os.WriteFile(path+".bak."+stamp,oldDirectory,0600);err!=nil{return "",err}
 }
 restore:=func()error{
  var errs []string
  if configChanged{if e:=writeAtomically(configPath,original,st.Mode().Perm());e!=nil{errs=append(errs,e.Error())}}
  if oldDirectory==nil {
   if e:=os.Remove(path);e!=nil&&!os.IsNotExist(e){errs=append(errs,e.Error())}
  }else if e:=writeAtomically(path,oldDirectory,0600);e!=nil{errs=append(errs,e.Error())}
  if len(errs)>0{return errors.New(strings.Join(errs,"; "))}
  return nil
 }
 if err=writeAtomically(path,nextDir,0600);err!=nil{return "",err}
 if configChanged {
  if err=writeAtomically(configPath,nextConfig,st.Mode().Perm());err!=nil{
   if recovery:=restore();recovery!=nil{return "",fmt.Errorf("%v；恢复失败：%v",err,recovery)}
   return "",err
  }
  if err=runChecked("systemctl","restart","xray");err==nil{
   err=runChecked("systemctl","is-active","--quiet","xray")
  }
  if err!=nil{
   failure:=err
   recovery:=restore()
   restartErr:=runChecked("systemctl","restart","xray")
   if recovery!=nil||restartErr!=nil{return "",fmt.Errorf("重启失败：%v；恢复文件：%v；恢复服务：%v",failure,recovery,restartErr)}
   return "",fmt.Errorf("Xray 未通过启动检测；已回滚：%w",failure)
  }
 }
 return message,nil
}
