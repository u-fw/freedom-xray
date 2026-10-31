package main

import (
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "sort"
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
 if err=json.Unmarshal(data,&s);err!=nil{return s,nil,fmt.Errorf("Suspended-user store is corrupted: %w",err)}
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
 if !managedUserName.MatchString(change.User){return "",errors.New("Invalid user identifier")}
 inbounds,err:=usableInbounds(root);if err!=nil{return "",err}
 if store.Users==nil{store.Users=map[string]suspendedAccount{}}
 if change.Kind=="disable" {
  if _,exists:=store.Users[change.User];exists{return "",errors.New("User is already suspended")}
  ids,err:=managedUserIDs(inbounds,change.User);if err!=nil{return "",err}
  if len(ids)!=1{return "",errors.New("User not found or UUID mismatch across inbounds; refusing suspension")}
  var uuid string;for id:=range ids{uuid=id}
  parked:=suspendedAccount{UUID:uuid,Nodes:map[string]map[string]any{}}
  // Validate every affected inbound before changing any of them.
  for _,in:=range inbounds {
   tag,_:=in["tag"].(string)
   list,_:=clientList(in)
   matches:=0
   for _,v:=range list{
    c,_:=getMap(v);email,_:=c["email"].(string)
    if bindingOwner(email)==change.User {
     matches++
     if c["id"]!=uuid{return "",errors.New("UUID conflict")}
     parked.Nodes[tag]=c
    }
   }
   if matches>1{return "",fmt.Errorf("Duplicate user entry in inbound %v",in["tag"])}
   if matches>0 && len(list)<=1{return "",fmt.Errorf("Inbound %v has only this user; refusing to suspend the last client",in["tag"])}
  }
  if len(parked.Nodes)==0{return "",errors.New("User not found")}
  for _,in:=range inbounds {
   settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   next:=make([]any,0,len(list))
   for _,v:=range list{
    c,_:=getMap(v);email,_:=c["email"].(string)
    if bindingOwner(email)!=change.User {next=append(next,v)}
   }
   settings["clients"]=next
  }
  store.Users[change.User]=parked
  return fmt.Sprintf("Suspended %s across %d inbounds; the original UUID was saved locally",change.User,len(parked.Nodes)),nil
 }
 if change.Kind=="enable" {
  parked,exists:=store.Users[change.User];if !exists{return "",errors.New("No suspended-user record found")}
  if parked.UUID==""||len(parked.Nodes)==0{return "",errors.New("Suspension record is missing a UUID or original inbounds")}
  ids,err:=managedUserIDs(inbounds,change.User);if err!=nil{return "",err}
  if len(ids)>0{return "",errors.New("An active user already has this name; refusing restore")}
  byTag:=make(map[string]map[string]any)
  for _,in:=range inbounds{tag,ok:=in["tag"].(string);if ok{byTag[tag]=in}}
  restored:=make(map[string]map[string]any,len(parked.Nodes))
  for tag,client:=range parked.Nodes {
   in,ok:=byTag[tag];if !ok{return "",fmt.Errorf("Original inbound %s was deleted; refusing partial restore",tag)}
   if err:=validateNodeTag(tag);err!=nil{return "",fmt.Errorf("Suspended record uses an invalid inbound tag %q: %w",tag,err)}
   email,_:=client["email"].(string)
   if email!=change.User {
    owner,bindingTag,binding:=parseBindingEmail(email)
    if !binding||owner!=change.User||bindingTag!=tag{return "",errors.New("Suspension record identity mismatch")}
   }
   if client["id"]!=parked.UUID{return "",errors.New("Suspension record identity mismatch")}
   list,_:=clientList(in)
   for _,v:=range list{
    c,_:=getMap(v);existingEmail,_:=c["email"].(string)
    if c["id"]==parked.UUID {return "",fmt.Errorf("UUID is already in use on inbound %s",tag)}
    if bindingOwner(existingEmail)==change.User{return "",fmt.Errorf("A client for this user already exists on inbound %s",tag)}
   }
   restoredClient:=make(map[string]any,len(client))
   for key,value:=range client{restoredClient[key]=value}
   restored[tag]=restoredClient
  }
  for tag,client:=range restored{
   in:=byTag[tag];settings,_:=getMap(in["settings"]);list,_:=clientList(in)
   settings["clients"]=append(list,client)
  }
  delete(store.Users,change.User)
  return fmt.Sprintf("Restored %s's %d inbounds and original UUID",change.User,len(parked.Nodes)),nil
 }
 return "",errors.New("Unknown suspend/restore action")
}

// Both files are backed up and restored together if a write or restart fails.
// The shared file lock prevents simultaneous Telegram changes.
func changeUserSuspension(configPath,xrayBinary,statsServer string,change UserChange) (string,error) {
 lock,err:=acquireConfigLock(configPath,20*time.Second);if err!=nil{return "",err}
 defer releaseConfigLock(lock)
 return changeUserSuspensionLocked(configPath,xrayBinary,statsServer,change)
}

func changeUserSuspensionLocked(configPath,xrayBinary,statsServer string,change UserChange) (string,error) {
 original,err:=os.ReadFile(configPath);if err!=nil{return "",err}
 st,err:=os.Stat(configPath);if err!=nil{return "",err}
 store,oldState,err:=readSuspendedStore(configPath);if err!=nil{return "",err}
 var root map[string]any
 if err=json.Unmarshal(original,&root);err!=nil{return "",err}
 message,err:=applyUserSuspension(root,&store,change);if err!=nil{return "",err}
 next,err:=json.MarshalIndent(root,"","  ");if err!=nil{return "",err}
 next=append(next,10)
 newState,err:=json.MarshalIndent(store,"","  ");if err!=nil{return "",err}
 newState=append(newState,10)
 // Check a staged config before modifying either original.
 check,err:=os.CreateTemp(filepath.Dir(configPath),".freedom-check-*.json");if err!=nil{return "",err}
 defer os.Remove(check.Name())
 if err=check.Chmod(st.Mode().Perm());err!=nil{check.Close();return "",err}
 if _,err=check.Write(next);err!=nil{check.Close();return "",err}
 if err=check.Close();err!=nil{return "",err}
 if err=runChecked(xrayBinary,"run","-test","-config",check.Name());err!=nil{return "",fmt.Errorf("Xray configuration test failed: %w",err)}
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
  if cfgErr!=nil || stateErr!=nil{return fmt.Errorf("Unable to restore files: config=%v, store=%v",cfgErr,stateErr)}
  return nil
 }
 if err=writeAtomically(statePath,newState,0600);err!=nil{
  if oldState==nil{_ = os.Remove(statePath)}
  return "",err
 }
 if err=writeAtomically(configPath,next,st.Mode().Perm());err!=nil{
  if restoreErr:=restore();restoreErr!=nil{return "",fmt.Errorf("Failed to write config: %w; rollback failed: %v",err,restoreErr)}
  return "",err
 }
 snapshotTrafficBestEffortLocked(configPath,xrayBinary,statsServer)
 if err=runChecked("systemctl","restart","xray");err==nil{err=runChecked("systemctl","is-active","--quiet","xray")}
 if err!=nil {
  failure:=err
  if restoreErr:=restore();restoreErr!=nil{return "",fmt.Errorf("Xray startup failed: %v; rollback also failed: %v",failure,restoreErr)}
  restartErr:=runChecked("systemctl","restart","xray")
  return "",fmt.Errorf("Operation failed; original config restored and Xray restart attempted: %v (service restore: %v)",failure,restartErr)
 }
 return message,nil
}

// Keep the existing management interface and route only suspend/resume to
// the two-file transaction. Every other operation, including creating an
// unbound user, goes through the directory transaction: it persists
// freedom-users.json and rejects names held by suspended accounts. Routing
// add through commitManagedConfig instead skipped the sidecar and failed with
// "No configuration changes" whenever no inbound was selected.
func changeManagedUser(configPath,xrayBinary,statsServer string,change UserChange)(string,error){
 if change.Kind=="disable"||change.Kind=="enable"{return changeUserSuspension(configPath,xrayBinary,statsServer,change)}
 return ChangeUserConfig(configPath,xrayBinary,statsServer,change)
}

// Prevent stale disabled accounts from being mistaken for active accounts
// when rendering Telegram summaries.
func isSuspendedUser(configPath,name string) bool {
 store,_,err:=readSuspendedStore(configPath)
 if err!=nil{return false}
 _,ok:=store.Users[name]
 return ok
}

