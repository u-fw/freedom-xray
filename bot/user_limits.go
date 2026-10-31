package main

import (
 "encoding/json"
 "errors"
 "fmt"
 "log"
 "math"
 "os"
 "path/filepath"
 "strconv"
 "strings"
 "sync"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Limits are local enforcement metadata: Xray itself has no quota or expiry
// fields, so the Bot stores them beside the config and suspends a client with
// the existing, reversible suspension flow when a limit is reached.
const gibBytes int64=1024*1024*1024
const expiryWarnWindow int64=3*24*3600

type userLimit struct {
 QuotaBytes int64 `json:"quota_bytes,omitempty"`
 ExpiryUnix int64 `json:"expiry_unix,omitempty"`
 Notified80 bool `json:"notified_80,omitempty"`
 NotifiedQuota bool `json:"notified_quota,omitempty"`
 NotifiedExpiry bool `json:"notified_expiry,omitempty"`
 NotifiedExpired bool `json:"notified_expired,omitempty"`
}

func (l userLimit) empty() bool {return l.QuotaBytes<=0&&l.ExpiryUnix<=0}

type userLimitStore struct{ Users map[string]userLimit `json:"users"` }

func userLimitStorePath(configPath string) string {
 return filepath.Join(filepath.Dir(configPath),"freedom-user-limits.json")
}

func readUserLimitStore(path string)(userLimitStore,error){
 store:=userLimitStore{Users:map[string]userLimit{}}
 data,err:=os.ReadFile(path)
 if os.IsNotExist(err){return store,nil}
 if err!=nil{return store,err}
 if err=json.Unmarshal(data,&store);err!=nil{return store,fmt.Errorf("User-limit store is corrupted: %w",err)}
 if store.Users==nil{store.Users=map[string]userLimit{}}
 return store,nil
}

func writeUserLimitStore(path string,store userLimitStore)error{
 data,err:=json.MarshalIndent(store,"","  ")
 if err!=nil{return err}
 data=append(data,'\n')
 return writeAtomically(path,data,0600)
}

var userLimitMu sync.Mutex

func updateUserLimits(configPath string,apply func(map[string]userLimit)error)error{
 lock,err:=acquireConfigLock(configPath,20*time.Second)
 if err!=nil{return err}
 defer releaseConfigLock(lock)
 return updateUserLimitsLocked(configPath,apply)
}

func updateUserLimitsLocked(configPath string,apply func(map[string]userLimit)error)error{
 userLimitMu.Lock()
 defer userLimitMu.Unlock()
 path:=userLimitStorePath(configPath)
 store,err:=readUserLimitStore(path)
 if err!=nil{return err}
 if err=apply(store.Users);err!=nil{return err}
 return writeUserLimitStore(path,store)
}

func snapshotUserLimits(configPath string)(map[string]userLimit,error){
 userLimitMu.Lock()
 defer userLimitMu.Unlock()
 store,err:=readUserLimitStore(userLimitStorePath(configPath))
 if err!=nil{return nil,err}
 return store.Users,nil
}

func markUserLimit(configPath,email string,apply func(*userLimit)){
 err:=updateUserLimits(configPath,func(users map[string]userLimit)error{
  limit,ok:=users[email]
  if !ok{return nil}
  apply(&limit)
  users[email]=limit
  return nil
 })
 if err!=nil{log.Printf("[Limits] update for %s failed: %v",email,err)}
}

func quotaLabel(limit userLimit,lang string)string{
 if limit.QuotaBytes<=0{return tr(lang,"limit.unlimited")}
 return FormatBytes(uint64(limit.QuotaBytes))
}

func expiryLabel(limit userLimit,lang string)string{
 if limit.ExpiryUnix<=0{return tr(lang,"limit.never")}
 return time.Unix(limit.ExpiryUnix,0).UTC().Format("2006-01-02")
}

// parseQuotaGB accepts a positive GiB value for the user's total traffic, or
// 0/- to remove the quota. Decimal values such as 0.5 are supported.
func parseQuotaGB(input string)(bytes int64,clear bool,err error){
 text:=strings.TrimSpace(input)
 if text=="0"||text=="-"{return 0,true,nil}
 value,parseErr:=strconv.ParseFloat(text,64)
 if parseErr!=nil||math.IsNaN(value)||math.IsInf(value,0)||value<=0{
  return 0,false,errors.New("Quota must be a positive number of GB, or 0 to clear")
 }
 if value>1048576{return 0,false,errors.New("Quota is too large")}
 bytes=int64(math.Round(value*float64(gibBytes)))
 if bytes<=0{return 0,false,errors.New("Quota is too small")}
 return bytes,false,nil
}

// parseExpiryInput accepts YYYY-MM-DD (UTC end of that day), Nd for N days
// from now, or 0/- to remove the expiry. Past dates are rejected.
func parseExpiryInput(input string,now time.Time)(unix int64,clear bool,err error){
 text:=strings.TrimSpace(input)
 if text=="0"||text=="-"{return 0,true,nil}
 if strings.HasSuffix(text,"d")||strings.HasSuffix(text,"D") {
  days,parseErr:=strconv.Atoi(text[:len(text)-1])
  if parseErr!=nil||days<0||days>36500{return 0,false,errors.New("Days must be between 0 and 36500")}
  if days==0{return 0,true,nil}
  return now.UTC().AddDate(0,0,days).Unix(),false,nil
 }
 date,parseErr:=time.Parse("2006-01-02",text)
 if parseErr!=nil{return 0,false,errors.New("Use YYYY-MM-DD or a number of days such as 30d")}
 end:=date.Add(24*time.Hour-time.Second).Unix()
 if end<=now.Unix(){return 0,false,errors.New("Expiry must be in the future")}
 return end,false,nil
}

// userLimitAction is the decision for one user at one sample. Disable carries
// the reason so the notification explains what happened.
type userLimitAction struct {
 Disable bool
 Reason string
 Warn80 bool
 WarnQuota bool
 WarnExpiry bool
 WarnExpired bool
}

func evaluateUserLimit(limit userLimit,usedBytes uint64,now time.Time)userLimitAction{
 var action userLimitAction
 t:=now.Unix()
 if limit.ExpiryUnix>0 {
  if t>=limit.ExpiryUnix {
   action.Disable=true
   action.Reason="expired"
   action.WarnExpired=!limit.NotifiedExpired
   return action
  }
  if t>=limit.ExpiryUnix-expiryWarnWindow&&!limit.NotifiedExpiry {
   action.WarnExpiry=true
  }
 }
 if limit.QuotaBytes>0 {
  quota:=uint64(limit.QuotaBytes)
  if usedBytes>=quota {
   action.Disable=true
   action.Reason="quota"
   action.WarnQuota=!limit.NotifiedQuota
   return action
  }
  if usedBytes>=quota-quota/5&&!limit.Notified80 {
   action.Warn80=true
  }
 }
 return action
}

// limitCooldown prevents a failing config write from being retried every
// minute forever; a successful enforcement is recorded in the limits store.
var limitCooldown=struct{
 sync.Mutex
 next map[string]time.Time
}{next:map[string]time.Time{}}

func limitCooldownReady(email string,now time.Time)bool{
 limitCooldown.Lock()
 defer limitCooldown.Unlock()
 next,ok:=limitCooldown.next[email]
 if !ok{return true}
 if now.Before(next){return false}
 delete(limitCooldown.next,email)
 return true
}

func setLimitCooldown(email string,now time.Time){
 limitCooldown.Lock()
 limitCooldown.next[email]=now.Add(15*time.Minute)
 limitCooldown.Unlock()
}

func (s *BotService) notifyAdmins(text string){
 for _,adminID:=range s.cfg.AdminIDs {
  msg:=tgbotapi.NewMessage(adminID,text)
  msg.ParseMode="Markdown"
  if _,err:=s.bot.Send(msg);err!=nil{log.Printf("[Limits] notify %d failed: %v",adminID,err)}
 }
}

func (s *BotService) runLimitEnforcer(){
 // Let startup, the first traffic sample and policy repair settle first.
 select{
 case <-s.stopCh:return
 case <-time.After(45*time.Second):
 }
 ticker:=time.NewTicker(time.Minute)
 defer ticker.Stop()
 for {
  s.enforceUserLimitsOnce()
  select{
  case <-s.stopCh:return
  case <-ticker.C:
  }
 }
}

// Recheck both the limits and ledger while holding the same lock used by
// resets, limit edits and suspension. A decision made from an older sample
// must not suspend a user whose quota was raised or traffic was reset.
func recheckAndSuspendLimitedUser(configPath,xrayBinary,statsServer,email string,now time.Time)(userLimitAction,uint64,bool,error){
 lock,err:=acquireConfigLock(configPath,20*time.Second)
 if err!=nil{return userLimitAction{},0,false,err}
 defer releaseConfigLock(lock)
 limits,err:=snapshotUserLimits(configPath)
 if err!=nil{return userLimitAction{},0,false,err}
 limit,exists:=limits[email]
 if !exists||limit.empty(){return userLimitAction{},0,false,nil}
 state,trafficErr:=loadTrafficStore(configPath)
 used:=uint64(0)
 if trafficErr!=nil {
  limit.QuotaBytes=0
 }else if counter,ok:=state.Users[email];ok {
  used=counter.TotalUp+counter.TotalDown
 }
 action:=evaluateUserLimit(limit,used,now)
 if !action.Disable{return action,used,false,nil}
 if _,err=changeUserSuspensionLocked(configPath,xrayBinary,statsServer,UserChange{Kind:"disable",User:email});err!=nil{
  return action,used,false,err
 }
 return action,used,true,nil
}

func (s *BotService) enforceUserLimitsOnce(){
 if !s.cfg.AllowMutations{return}
 limits,err:=snapshotUserLimits(s.cfg.XrayConfigPath)
 if err!=nil {
  log.Printf("[Limits] store unavailable: %v",err)
  return
 }
 if len(limits)==0{return}
 state,err:=loadTrafficStore(s.cfg.XrayConfigPath)
 trafficAvailable:=err==nil
 if err!=nil {
  log.Printf("[Limits] traffic store unavailable: %v",err)
 }
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil {
  log.Printf("[Limits] inbound read failed: %v",err)
  return
 }
 active:=map[string]bool{}
 for _,node:=range nodes {
  for _,client:=range node.Clients {
   if client.Email!=""{active[bindingOwner(client.Email)]=true}
  }
 }
 now:=time.Now()
 lang:=s.cfg.Language
 for email,limit:=range limits {
  if limit.empty()||!active[email]{continue}
  used:=uint64(0)
  if counter,ok:=state.Users[email];ok{used=counter.TotalUp+counter.TotalDown}
  evaluatedLimit:=limit
  if !trafficAvailable {evaluatedLimit.QuotaBytes=0}
  action:=evaluateUserLimit(evaluatedLimit,used,now)
  // Usage below the warning threshold clears stale flags after a reset or a
  // raised quota, so the next threshold crossing notifies again.
  if trafficAvailable&&limit.QuotaBytes>0&&used<uint64(limit.QuotaBytes)-uint64(limit.QuotaBytes)/5&&(limit.Notified80||limit.NotifiedQuota) {
   markUserLimit(s.cfg.XrayConfigPath,email,func(l *userLimit){l.Notified80=false;l.NotifiedQuota=false})
  }
  if action.Disable {
   if !limitCooldownReady(email,now){continue}
   confirmed,confirmedUsed,suspended,err:=recheckAndSuspendLimitedUser(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer,email,time.Now())
   if err!=nil {
    log.Printf("[Limits] auto-suspend %s failed: %v",email,err)
    setLimitCooldown(email,now)
    s.notifyAdmins(tr(lang,"limit.enforce.failed",mdCode(email),mdEsc(err.Error())))
    continue
   }
   if !suspended{continue}
   action=confirmed
   used=confirmedUsed
   switch action.Reason {
   case "expired":
    markUserLimit(s.cfg.XrayConfigPath,email,func(l *userLimit){l.NotifiedExpired=true;l.NotifiedExpiry=true})
    s.notifyAdmins(tr(lang,"limit.notify.expired",mdCode(email)))
   default:
    markUserLimit(s.cfg.XrayConfigPath,email,func(l *userLimit){l.NotifiedQuota=true;l.Notified80=true})
    s.notifyAdmins(tr(lang,"limit.notify.quota",mdCode(email),mdCode(FormatBytes(used))))
   }
   continue
  }
  if action.Warn80 {
   markUserLimit(s.cfg.XrayConfigPath,email,func(l *userLimit){l.Notified80=true})
   s.notifyAdmins(tr(lang,"limit.notify.80",mdCode(email),mdCode(FormatBytes(used)),mdCode(FormatBytes(uint64(limit.QuotaBytes)))))
  }
  if action.WarnExpiry {
   markUserLimit(s.cfg.XrayConfigPath,email,func(l *userLimit){l.NotifiedExpiry=true})
   s.notifyAdmins(tr(lang,"limit.notify.expiry",mdCode(email),mdCode(expiryLabel(limit,lang))))
  }
 }
}

type userLimitWizard struct{Email string;Kind string;AdminID int64;Expires time.Time}

var userLimitWizards=struct{
 sync.Mutex
 items map[int64]userLimitWizard
}{items:map[int64]userLimitWizard{}}

func (s *BotService) beginUserLimitWizard(chatID,adminID int64,msgID int,email,kind,prompt string){
 userLimitWizards.Lock()
 userLimitWizards.items[chatID]=userLimitWizard{Email:email,Kind:kind,AdminID:adminID,Expires:time.Now().Add(5*time.Minute)}
 userLimitWizards.Unlock()
 s.sendOrEdit(chatID,msgID,prompt,s.backToMainMarkup())
}

func (s *BotService) handleUserLimitInput(msg *tgbotapi.Message)bool{
 chatID:=msg.Chat.ID
 userLimitWizards.Lock()
 state,ok:=userLimitWizards.items[chatID]
 if !ok{userLimitWizards.Unlock();return false}
 if time.Now().After(state.Expires)||state.AdminID!=msg.From.ID {
  delete(userLimitWizards.items,chatID)
  userLimitWizards.Unlock()
  return false
 }
 if strings.TrimSpace(msg.Text)=="/cancel" {
  delete(userLimitWizards.items,chatID)
  userLimitWizards.Unlock()
  s.sendAlert(chatID,tr(s.cfg.Language,"limit.cancelled"))
  return true
 }
 if msg.Command()!=""{userLimitWizards.Unlock();return false}
 delete(userLimitWizards.items,chatID)
 userLimitWizards.Unlock()
 if !s.cfg.AllowMutations {
  s.sendAlert(chatID,tr(s.cfg.Language,"manage.disabled"))
  return true
 }
 value:=strings.TrimSpace(msg.Text)
 key:=userCallbackKey(state.Email)
 switch state.Kind {
 case "quota":
  bytes,clear,err:=parseQuotaGB(value)
  if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"limit.quotainvalid"));return true}
  err=updateUserLimits(s.cfg.XrayConfigPath,func(users map[string]userLimit)error{
   limit:=users[state.Email]
   if clear {
    limit.QuotaBytes=0
   }else{
    limit.QuotaBytes=bytes
   }
   limit.Notified80=false
   limit.NotifiedQuota=false
   if limit.empty(){delete(users,state.Email)}else{users[state.Email]=limit}
   return nil
  })
  if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"limit.savefailed",err.Error()));return true}
  label:=tr(s.cfg.Language,"limit.unlimited")
  if !clear{label=FormatBytes(uint64(bytes))}
  s.sendAlert(chatID,tr(s.cfg.Language,"limit.quotaset",state.Email,label))
  s.sendUserManageCard(chatID,0,key)
 case "expiry":
  stamp,clear,err:=parseExpiryInput(value,time.Now())
  if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"limit.expiryinvalid"));return true}
  err=updateUserLimits(s.cfg.XrayConfigPath,func(users map[string]userLimit)error{
   limit:=users[state.Email]
   if clear {
    limit.ExpiryUnix=0
   }else{
    limit.ExpiryUnix=stamp
   }
   limit.NotifiedExpiry=false
   limit.NotifiedExpired=false
   if limit.empty(){delete(users,state.Email)}else{users[state.Email]=limit}
   return nil
  })
  if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"limit.savefailed",err.Error()));return true}
  label:=tr(s.cfg.Language,"limit.never")
  if !clear{label=time.Unix(stamp,0).UTC().Format("2006-01-02")}
  s.sendAlert(chatID,tr(s.cfg.Language,"limit.expiryset",state.Email,label))
  s.sendUserManageCard(chatID,0,key)
 default:
  s.sendAlert(chatID,tr(s.cfg.Language,"limit.unknown"))
 }
 return true
}

func (s *BotService) clearUserLimits(chatID int64,msgID int,key,email string){
 if !s.cfg.AllowMutations {
  s.sendAlert(chatID,tr(s.cfg.Language,"manage.disabled"))
  return
 }
 limits,readErr:=snapshotUserLimits(s.cfg.XrayConfigPath)
 if readErr!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"limit.savefailed",readErr.Error()));return}
 if _,ok:=limits[email];!ok{
  s.sendAlert(chatID,tr(s.cfg.Language,"limit.none",email))
  s.sendUserManageCard(chatID,msgID,key)
  return
 }
 err:=updateUserLimits(s.cfg.XrayConfigPath,func(users map[string]userLimit)error{
  delete(users,email)
  return nil
 })
 if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"limit.savefailed",err.Error()));return}
 s.sendAlert(chatID,tr(s.cfg.Language,"limit.cleared",email))
 s.sendUserManageCard(chatID,msgID,key)
}

// moveUserLimit removes metadata for a deleted user or moves it on rename.
// It is called by the shared local and Telegram management path.
func moveUserLimit(configPath,email,newName string)error{
 return updateUserLimitsLocked(configPath,func(users map[string]userLimit)error{
  limit,ok:=users[email]
  if !ok {
   if newName!=""{delete(users,newName)}
   return nil
  }
  delete(users,email)
  if newName!="" {
   limit.Notified80=false
   limit.NotifiedQuota=false
   limit.NotifiedExpiry=false
   limit.NotifiedExpired=false
   users[newName]=limit
  }
  return nil
 })
}
