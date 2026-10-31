package main

import (
 "bytes"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "sort"
 "strings"
 "time"
)

// bindingEmailsMarker records the one-time transition to per-inbound client
// identities. "1" means the migration ran; "0" means an administrator
// explicitly reverted it, so startup must not immediately migrate again.
// An absent marker still triggers a scan: a shell-added plain client after the
// first migration must be converted on the next startup.
const bindingEmailsMarker="binding_emails_v1"

type bindingMigrationMode int

const (
 bindingMigrate bindingMigrationMode=iota
 bindingRevert
)

type bindingMigrationStats struct {
 Inbounds int `json:"inbounds"`
 Clients int `json:"clients"`
 Rewritten int `json:"rewritten"`
 AlreadyBound int `json:"already_bound"`
 Custom int `json:"custom"`
 SuspendedRewritten int `json:"suspended_rewritten"`
 SuspendedSkipped []string `json:"suspended_skipped"`
 BackupPath string `json:"backup_path,omitempty"`
}

func (s bindingMigrationStats) changed() bool {
 return s.Rewritten>0||s.SuspendedRewritten>0
}

func (s bindingMigrationStats) String() string {
 return fmt.Sprintf("inbounds=%d clients=%d rewritten=%d already_bound=%d custom=%d suspended_rewritten=%d",
  s.Inbounds,s.Clients,s.Rewritten,s.AlreadyBound,s.Custom,s.SuspendedRewritten)
}

// applyBindingEmailMigration rewrites managed client identities in place.
// Migration turns a plain managed username into <user>+<base64(tag)> for each
// inbound. Revert turns a canonical binding back into its owner. Custom
// emails, empty emails and bindings for another inbound are never guessed at.
func applyBindingEmailMigration(root map[string]any,store *suspendedStore,mode bindingMigrationMode)(bindingMigrationStats,error){
 var stats bindingMigrationStats
 raw,ok:=root["inbounds"].([]any)
 if !ok{return stats,errors.New("Missing inbounds")}
 tags:=map[string]bool{}
 ownerIDs:=map[string]string{}
 for _,value:=range raw {
  in,ok:=getMap(value)
  if !ok{return stats,errors.New("Invalid inbound format")}
  if in["tag"]=="api-internal"||in["protocol"]!="vless" {continue}
  settings,ok:=getMap(in["settings"])
  if !ok{continue}
  list,ok:=settings["clients"].([]any)
  if !ok||len(list)==0{continue}
  tag,_:=in["tag"].(string)
  if err:=validateNodeTag(tag);err!=nil {
   return stats,fmt.Errorf("Inbound tag %q cannot be used for per-node accounting: %w",tag,err)
  }
  if tags[tag]{return stats,fmt.Errorf("Duplicate inbound tag %q; refusing binding migration",tag)}
  tags[tag]=true
  stats.Inbounds++
  seen:=map[string]bool{}
  for _,entry:=range list {
   client,ok:=getMap(entry)
   if !ok{return stats,errors.New("Invalid client format")}
   stats.Clients++
   email,_:=client["email"].(string)
   uuid,_:=client["id"].(string)
   target:=email
   owner:=""
   switch mode {
   case bindingMigrate:
    if email=="" {break}
    if parsedOwner,boundTag,isBinding:=parseBindingEmail(email);isBinding {
     if boundTag!=tag {
      return stats,fmt.Errorf("Client %q in inbound %s is bound to inbound %s; refusing migration",email,tag,boundTag)
     }
     owner=parsedOwner
     stats.AlreadyBound++
    }else if managedUserName.MatchString(email) {
     owner=email
     target=bindingEmail(email,tag)
     client["email"]=target
     stats.Rewritten++
    }else{
     stats.Custom++
    }
   case bindingRevert:
    if parsedOwner,boundTag,isBinding:=parseBindingEmail(email);isBinding {
     if boundTag!=tag {
      return stats,fmt.Errorf("Client %q in inbound %s is bound to inbound %s; refusing revert",email,tag,boundTag)
     }
     owner=parsedOwner
     target=parsedOwner
     client["email"]=target
     stats.Rewritten++
    }else if email!=""&&managedUserName.MatchString(email) {
     owner=email
    }
   default:
    return stats,errors.New("Unknown binding migration mode")
   }
   if target!="" {
    if seen[target] {
     return stats,fmt.Errorf("Duplicate client email %q in inbound %s; refusing binding migration",target,tag)
    }
    seen[target]=true
   }
   if owner!=""&&uuid!="" {
    if previous,exists:=ownerIDs[owner];exists&&previous!=uuid {
     return stats,fmt.Errorf("User %s has different UUIDs across inbounds; refusing binding migration",owner)
    }
    ownerIDs[owner]=uuid
   }
  }
 }
 suspended:=normalizeSuspendedBindings(store,mode)
 stats.SuspendedRewritten=suspended.rewritten
 stats.SuspendedSkipped=suspended.skipped
 return stats,nil
}

type suspendedBindingResult struct {
 rewritten int
 skipped []string
}

// Suspended clients are keyed by user and inbound tag, so their parked email
// can be normalized without the live configuration. Unrecognized records are
// preserved and reported rather than silently rewritten to another identity.
func normalizeSuspendedBindings(store *suspendedStore,mode bindingMigrationMode)suspendedBindingResult {
 var result suspendedBindingResult
 if store==nil||len(store.Users)==0{return result}
 names:=make([]string,0,len(store.Users))
 for name:=range store.Users{names=append(names,name)}
 sort.Strings(names)
 for _,user:=range names {
  account:=store.Users[user]
  if len(account.Nodes)==0{continue}
  if !managedUserName.MatchString(user){continue}
  tags:=make([]string,0,len(account.Nodes))
  for tag:=range account.Nodes{tags=append(tags,tag)}
  sort.Strings(tags)
  for _,tag:=range tags {
   client:=account.Nodes[tag]
   if client==nil{continue}
   email,_:=client["email"].(string)
   switch mode {
   case bindingMigrate:
    if err:=validateNodeTag(tag);err!=nil {
     result.skipped=append(result.skipped,fmt.Sprintf("%s/%s: invalid tag",user,tag))
     continue
    }
    if email=="" {
     client["email"]=bindingEmail(user,tag)
     result.rewritten++
     continue
    }
    if email==user {
     client["email"]=bindingEmail(user,tag)
     result.rewritten++
     continue
    }
    if owner,boundTag,ok:=parseBindingEmail(email);ok&&owner==user&&boundTag==tag {continue}
    result.skipped=append(result.skipped,fmt.Sprintf("%s/%s: unrecognized email",user,tag))
   case bindingRevert:
    if email=="" {
     client["email"]=user
     result.rewritten++
     continue
    }
    if email==user {continue}
    if owner,boundTag,ok:=parseBindingEmail(email);ok&&owner==user&&boundTag==tag {
     client["email"]=user
     result.rewritten++
     continue
    }
    result.skipped=append(result.skipped,fmt.Sprintf("%s/%s: unrecognized email",user,tag))
   }
  }
 }
 return result
}

func decodeBindingMigrationConfig(configPath string)(map[string]any,error) {
 data,err:=os.ReadFile(configPath)
 if err!=nil{return nil,err}
 var root map[string]any
 decoder:=json.NewDecoder(bytes.NewReader(data))
 decoder.UseNumber()
 if err=decoder.Decode(&root);err!=nil{return nil,err}
 return root,nil
}

// preflightBindingMigration applies the same transformation to in-memory
// copies so startup can skip the transaction, and therefore the Xray restart,
// when neither the configuration nor the suspension store needs a change.
func preflightBindingMigration(configPath string,mode bindingMigrationMode)(bindingMigrationStats,error){
 root,err:=decodeBindingMigrationConfig(configPath)
 if err!=nil{return bindingMigrationStats{},err}
 store,_,err:=readSuspendedStore(configPath)
 if err!=nil{return bindingMigrationStats{},err}
 return applyBindingEmailMigration(root,&store,mode)
}

// backupTrafficDBBeforeBindings keeps a consistent pre-migration ledger
// snapshot. VACUUM INTO writes a complete database rather than copying a live
// file whose WAL may still hold committed rows.
func backupTrafficDBBeforeBindings(configPath string)(string,error){
 dbPath:=trafficDBPath(configPath)
 if _,err:=os.Stat(dbPath);errors.Is(err,os.ErrNotExist){return "",nil}else if err!=nil{return "",err}
 backup:=dbPath+".pre-bindings.bak"
 if _,err:=os.Stat(backup);err==nil {
  if err=os.Chmod(backup,0600);err!=nil{return "",err}
  return backup,nil
 }else if !errors.Is(err,os.ErrNotExist) {
  return "",err
 }
 db,err:=openTrafficDB(configPath)
 if err!=nil{return "",err}
 defer db.Close()
 if _,err=db.Exec("PRAGMA wal_checkpoint(FULL)");err!=nil {
  return "",fmt.Errorf("checkpoint traffic database: %w",err)
 }
 // VACUUM INTO accepts an empty target. Create it privately before SQLite can
 // write accounting history, even when the caller has a permissive umask.
 file,err:=os.OpenFile(backup,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600)
 if err!=nil{return "",err}
 if err=file.Close();err!=nil{os.Remove(backup);return "",err}
 completed:=false
 defer func(){if !completed{os.Remove(backup)}}()
 if _,err=db.Exec("VACUUM INTO ?",backup);err!=nil {
  return "",fmt.Errorf("backup traffic database: %w",err)
 }
 if err=os.Chmod(backup,0600);err!=nil{return "",err}
 completed=true
 return backup,nil
}

func marshalSuspendedStore(store suspendedStore)([]byte,error){
 data,err:=json.MarshalIndent(store,"","  ")
 if err!=nil{return nil,err}
 return append(data,'\n'),nil
}

func migrateBindingEmails(configPath,xrayBinary,statsServer string)(bindingMigrationStats,error){
 return migrateBindingEmailsWithForce(configPath,xrayBinary,statsServer,false)
}

// migrateBindingEmailsForced is the explicit CLI path: it overrides a marker
// of "0" left by -local-revert-bindings.
func migrateBindingEmailsForced(configPath,xrayBinary,statsServer string)(bindingMigrationStats,error){
 return migrateBindingEmailsWithForce(configPath,xrayBinary,statsServer,true)
}

func migrateBindingEmailsWithForce(configPath,xrayBinary,statsServer string,force bool)(bindingMigrationStats,error){
 lock,err:=acquireConfigLock(configPath,20*time.Second)
 if err!=nil{return bindingMigrationStats{},err}
 defer releaseConfigLock(lock)
 marker,err:=trafficMetaValue(configPath,bindingEmailsMarker)
 if err!=nil{return bindingMigrationStats{},err}
 if marker=="0"&&!force{return bindingMigrationStats{},nil}
 stats,err:=preflightBindingMigration(configPath,bindingMigrate)
 if err!=nil{return stats,err}
 if !stats.changed(){
  if force {
   if err:=setTrafficMetaValue(configPath,bindingEmailsMarker,"1");err!=nil{return stats,err}
  }
  return stats,nil
 }
 backup,err:=backupTrafficDBBeforeBindings(configPath)
 if err!=nil{return stats,err}
 stats.BackupPath=backup

 var suspensionNext []byte
 suspensionChanged:=false
 var reported bindingMigrationStats
 _,err=commitManagedConfigTxLocked(configPath,xrayBinary,statsServer,[]configSidecar{{
  Path:suspendedStorePath(configPath),
  Transform:func(original []byte,exists bool)([]byte,error){
   if suspensionChanged{return suspensionNext,nil}
   if !exists{return nil,nil}
   return original,nil
  },
 }},func(root map[string]any)(string,error){
  store,_,readErr:=readSuspendedStore(configPath)
  if readErr!=nil{return "",readErr}
  result,applyErr:=applyBindingEmailMigration(root,&store,bindingMigrate)
  if applyErr!=nil{return "",applyErr}
  reported=result
  if result.SuspendedRewritten>0 {
   next,marshalErr:=marshalSuspendedStore(store)
   if marshalErr!=nil{return "",marshalErr}
   suspensionNext=next
   suspensionChanged=true
  }
  if result.Rewritten==0&&!suspensionChanged{return "",errNoConfigChanges}
  return fmt.Sprintf("Migrated %d client identity/identities across %d inbound(s) to per-node accounting",result.Rewritten,result.Inbounds),nil
 })
 if errors.Is(err,errNoConfigChanges) {
  reported.BackupPath=stats.BackupPath
  return reported,nil
 }
 if err!=nil {
  reported.BackupPath=stats.BackupPath
  return reported,err
 }
 reported.BackupPath=stats.BackupPath
 if err=setTrafficMetaValue(configPath,bindingEmailsMarker,"1");err!=nil {
  return reported,fmt.Errorf("binding migration succeeded but recording its marker failed: %w",err)
 }
 return reported,nil
}

func revertBindingEmails(configPath,xrayBinary,statsServer string)(bindingMigrationStats,error){
 lock,err:=acquireConfigLock(configPath,20*time.Second)
 if err!=nil{return bindingMigrationStats{},err}
 defer releaseConfigLock(lock)
 stats,err:=preflightBindingMigration(configPath,bindingRevert)
 if err!=nil{return stats,err}
 if !stats.changed(){
  if err:=setTrafficMetaValue(configPath,bindingEmailsMarker,"0");err!=nil{return stats,err}
  return stats,nil
 }
 backup,err:=backupTrafficDBBeforeBindings(configPath)
 if err!=nil{return stats,err}
 stats.BackupPath=backup
 var suspensionNext []byte
 suspensionChanged:=false
 var reported bindingMigrationStats
 _,err=commitManagedConfigTxLocked(configPath,xrayBinary,statsServer,[]configSidecar{{
  Path:suspendedStorePath(configPath),
  Transform:func(original []byte,exists bool)([]byte,error){
   if suspensionChanged{return suspensionNext,nil}
   if !exists{return nil,nil}
   return original,nil
  },
 }},func(root map[string]any)(string,error){
  store,_,readErr:=readSuspendedStore(configPath)
  if readErr!=nil{return "",readErr}
  result,applyErr:=applyBindingEmailMigration(root,&store,bindingRevert)
  if applyErr!=nil{return "",applyErr}
  reported=result
  if result.SuspendedRewritten>0 {
   next,marshalErr:=marshalSuspendedStore(store)
   if marshalErr!=nil{return "",marshalErr}
   suspensionNext=next
   suspensionChanged=true
  }
  if result.Rewritten==0&&!suspensionChanged{return "",errNoConfigChanges}
  return fmt.Sprintf("Reverted %d binding identity/identities across %d inbound(s) to plain usernames",result.Rewritten,result.Inbounds),nil
 })
 if errors.Is(err,errNoConfigChanges) {
  reported.BackupPath=stats.BackupPath
  return reported,nil
 }
 if err!=nil {
  reported.BackupPath=stats.BackupPath
  return reported,err
 }
 if err=setTrafficMetaValue(configPath,bindingEmailsMarker,"0");err!=nil {
  return reported,fmt.Errorf("binding revert succeeded but recording its marker failed: %w",err)
 }
 return reported,nil
}

// bindingMigrationWarning joins non-fatal suspension-record warnings for logs.
func bindingMigrationWarning(skipped []string) string {
 if len(skipped)==0{return ""}
 return strings.Join(skipped,"; ")
}
