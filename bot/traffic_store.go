package main

import (
 "context"
 "errors"
 "fmt"
 "log"
 "os/exec"
 "path/filepath"
 "sort"
 "strings"
 "time"
)

type trafficCounter struct {
 TotalUp uint64 `json:"total_up"`
 TotalDown uint64 `json:"total_down"`
 LastUp uint64 `json:"last_up"`
 LastDown uint64 `json:"last_down"`
 Epoch string `json:"epoch,omitempty"`
}

type trafficStore struct {
 Users map[string]trafficCounter `json:"users"`
 // Bindings holds per-inbound counters for users migrated to binding emails.
 // Users remains the authoritative aggregate and also contains pre-migration
 // history that cannot be split by node.
 Bindings map[string]trafficCounter `json:"bindings,omitempty"`
 Inbounds map[string]trafficCounter `json:"inbounds,omitempty"`
 UpdatedAt string `json:"updated_at,omitempty"`
 LastOnline map[string]int64 `json:"last_online,omitempty"`
 LastActive map[string]int64 `json:"last_active,omitempty"`
 OnlineIPs map[string][]onlineClientIP `json:"online_ips,omitempty"`
 LiveIPs map[string][]onlineClientIP `json:"live_ips,omitempty"`
 OnlinePollAt int64 `json:"online_poll_at,omitempty"`
}

func trafficStorePath(configPath string) string {
 return filepath.Join(filepath.Dir(configPath),"freedom-stats.json")
}

// systemd's InvocationID is unique per service activation; unlike counter
// decreases, it catches an Xray restart even if new counters exceed old ones.
// Empty means unavailable: revert to a conservative decrease-based heuristic.
func xrayStatsEpoch() string {
 ctx,cancel:=context.WithTimeout(context.Background(),2*time.Second)
 defer cancel()
 output,err:=exec.CommandContext(ctx,"systemctl","show","xray.service","-p","InvocationID","--value").Output()
 if err!=nil{return ""}
 value:=strings.TrimSpace(string(output))
 if len(value)!=32{return ""}
 for _,c:=range value {if !strings.ContainsRune("0123456789abcdefABCDEF",c){return ""}}
 return strings.ToLower(value)
}

func updatedCounter(old trafficCounter,up,down uint64,epoch string) trafficCounter {
 next:=old
 // For legacy stores (without epoch), preserve the old delta semantics.
 // For a new invocation count ALL new runtime traffic, even if it is larger.
 if epoch!="" && old.Epoch!="" && old.Epoch!=epoch {
  next.TotalUp+=up
  next.TotalDown+=down
 } else {
  if up>=old.LastUp {next.TotalUp+=up-old.LastUp} else {next.TotalUp+=up}
  if down>=old.LastDown {next.TotalDown+=down-old.LastDown} else {next.TotalDown+=down}
 }
 next.LastUp=up
 next.LastDown=down
 if epoch!="" {next.Epoch=epoch}
 return next
}

func applyTrafficSnapshot(state *trafficStore,users []UserTraffic,inbounds []InboundTraffic,epoch string) bool {
 ensureTrafficMaps(state)
 changed:=false
 for _,u:=range users {
  if u.Email==""{continue}
  if owner,_,ok:=parseBindingEmail(u.Email);ok {
   current,bindingExists:=state.Bindings[u.Email]
   next:=updatedCounter(current,u.Uplink,u.Downlink,epoch)
   if !bindingExists||next!=current {
    state.Bindings[u.Email]=next
    // Users keeps the rolling total every existing limit, quota, and
    // directory view already reads. Only the totals move: the binding's
    // LastUp/LastDown/Epoch describe the per-node runtime counter, while the
    // owner row stays a sum and must not produce a second delta calculation.
    aggregate:=state.Users[owner]
    aggregate.TotalUp+=next.TotalUp-current.TotalUp
    aggregate.TotalDown+=next.TotalDown-current.TotalDown
    state.Users[owner]=aggregate
    changed=true
   }
   continue
  }
  current,ok:=state.Users[u.Email]
  next:=updatedCounter(current,u.Uplink,u.Downlink,epoch)
  if !ok||next!=current{state.Users[u.Email]=next;changed=true}
 }
 for _,in:=range inbounds {
  if in.Tag==""||in.Tag=="api-internal"{continue}
  current,ok:=state.Inbounds[in.Tag]
  next:=updatedCounter(current,in.Uplink,in.Downlink,epoch)
  if !ok||next!=current{state.Inbounds[in.Tag]=next;changed=true}
 }
 return changed
}

// Caller MUST hold .freedom-config.lock so manual resets, rename, writes, and
// periodic samples do not overwrite each other's updates.
func snapshotTrafficLocked(configPath,xrayBinary,statsServer string) (trafficStore,error) {
 state,err:=loadTrafficStore(configPath)
 if err!=nil{return state,err}
 before:=xrayStatsEpoch()
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
 defer cancel()
 users,err:=QueryTrafficStats(ctx,xrayBinary,statsServer,false)
 if err!=nil{return state,fmt.Errorf("User traffic sampling failed: %w",err)}
 inbounds,err:=QueryInboundTraffic(ctx,xrayBinary,statsServer)
 if err!=nil{return state,fmt.Errorf("Inbound traffic sampling failed: %w",err)}
 after:=xrayStatsEpoch()
 if before!=""&&after!=""&&before!=after {
  return state,errors.New("Xray restarted during sampling; ignoring mixed-epoch statistics")
 }
 epoch:=after
 if epoch==""{epoch=before}
 previous:=make(map[string]trafficCounter,len(state.Users))
 for name,counter:=range state.Users{previous[name]=counter}
 changed:=applyTrafficSnapshot(&state,users,inbounds,epoch)
 // Collect online IPs independently of traffic accounting. Unsupported
 // APIs must not stop the durable traffic sampler.
 onlineCtx,onlineCancel:=context.WithTimeout(context.Background(),10*time.Second)
 defer onlineCancel()
 if observed,onlineErr:=queryOnlineClientsForEmails(onlineCtx,xrayBinary,statsServer,onlineQueryEmails(configPath,state));onlineErr==nil {
  if applyOnlineSnapshot(&state,observed,time.Now()){changed=true}
 }else if !isOnlineUnsupported(onlineErr){
  log.Printf("[Online] optional poll failed: %v",onlineErr)
 }
 if inferOnlineFromTraffic(&state,previous,time.Now()){changed=true}
 if changed {
  state.UpdatedAt=time.Now().UTC().Format(time.RFC3339)
  if err=saveTrafficStore(configPath,state);err!=nil{return state,fmt.Errorf("Failed to persist traffic counters: %w",err)}
 }
 return state,nil
}

func snapshotTraffic(configPath,xrayBinary,statsServer string)(trafficStore,error){
 lock,err:=acquireConfigLock(configPath,5*time.Second)
 if err!=nil{return trafficStore{},err}
 defer releaseConfigLock(lock)
 return snapshotTrafficLocked(configPath,xrayBinary,statsServer)
}

// onlineQueryEmails supplies the candidate list used only by the per-email
// compatibility path for cores without "statsonlineiplist -all". Reading the
// directory is best effort: the collector must never fail because the user
// directory is temporarily unreadable.
func onlineQueryEmails(configPath string,state trafficStore) []string {
 candidates:=make([]string,0,len(state.Users))
 for name:=range state.Users{candidates=append(candidates,name)}
 for email:=range state.Bindings{candidates=append(candidates,email)}
 if names,err:=allDirectoryNames(configPath);err==nil {
  candidates=append(candidates,names...)
 }
 if groups,err:=managedClientEmailsFromConfig(configPath);err==nil {
  for _,emails:=range groups{candidates=append(candidates,emails...)}
 }
 return sortedUniqueStrings(candidates)
}

func snapshotTrafficBestEffortLocked(configPath,xrayBinary,statsServer string) {
 if _,err:=snapshotTrafficLocked(configPath,xrayBinary,statsServer);err!=nil{
  log.Printf("[Traffic] Pre-restart snapshot failed; the last unsampled window may be lost: %v",err)
 }
}

func storedUserTraffic(state trafficStore) []UserTraffic {
 out:=make([]UserTraffic,0,len(state.Users))
 for name,c:=range state.Users {
  out=append(out,UserTraffic{Email:name,Uplink:c.TotalUp,Downlink:c.TotalDown,Total:c.TotalUp+c.TotalDown})
 }
 sort.Slice(out,func(i,j int)bool{
  if out[i].Total!=out[j].Total{return out[i].Total>out[j].Total}
  return out[i].Email<out[j].Email
 })
 return out
}
func storedInboundTraffic(state trafficStore) []InboundTraffic {
 out:=make([]InboundTraffic,0,len(state.Inbounds))
 for tag,c:=range state.Inbounds {out=append(out,InboundTraffic{Tag:tag,Uplink:c.TotalUp,Downlink:c.TotalDown})}
 sort.Slice(out,func(i,j int)bool{return out[i].Tag<out[j].Tag})
 return out
}

func editTrafficStore(configPath string,edit func(*trafficStore)error)error{
 lock,err:=acquireConfigLock(configPath,5*time.Second)
 if err!=nil{return err}
 defer releaseConfigLock(lock)
 state,err:=loadTrafficStore(configPath);if err!=nil{return err}
 if err=edit(&state);err!=nil{return err}
 return saveTrafficStore(configPath,state)
}

// Remove per-user accounting and IP history, preserving inbound totals.
func discardTrafficUser(state *trafficStore,name string) bool {
 changed:=false
 ensureTrafficMaps(state)
 if _,ok:=state.Users[name];ok{delete(state.Users,name);changed=true}
 for email:=range state.Bindings {
  if owner,_,ok:=parseBindingEmail(email);ok&&owner==name {
   delete(state.Bindings,email);changed=true
  }
 }
 if _,ok:=state.LastOnline[name];ok{delete(state.LastOnline,name);changed=true}
 if _,ok:=state.LastActive[name];ok{delete(state.LastActive,name);changed=true}
 if _,ok:=state.OnlineIPs[name];ok{delete(state.OnlineIPs,name);changed=true}
 if _,ok:=state.LiveIPs[name];ok{delete(state.LiveIPs,name);changed=true}
 return changed
}

// Must be called after a committed deletion under the config lock.
func purgeTrafficUserLocked(configPath,name string) error {
 state,err:=loadTrafficStore(configPath);if err!=nil{return err}
 if !discardTrafficUser(&state,name){return nil}
 return saveTrafficStore(configPath,state)
}

// Reconcile legacy deleted users only after loading authoritative config.
// Keep suspended users' historical totals and never touch inbound totals.
func reconcileTrafficUsersLocked(configPath string,state *trafficStore)(bool,error) {
 names,err:=allDirectoryNames(configPath);if err!=nil{return false,err}
 suspended,err:=disabledUserNames(configPath);if err!=nil{return false,err}
 allowed:=make(map[string]bool,len(names)+len(suspended))
 for _,name:=range names{allowed[name]=true}
 for _,name:=range suspended{allowed[name]=true}
 changed:=false
 for name:=range state.Users {if !allowed[name]&&discardTrafficUser(state,name){changed=true}}
 for email:=range state.Bindings {
  owner,_,ok:=parseBindingEmail(email)
  if ok&&!allowed[owner]&&discardTrafficUser(state,owner){changed=true}
 }
 for name:=range state.LiveIPs {if !allowed[name]&&discardTrafficUser(state,name){changed=true}}
 for name:=range state.OnlineIPs {if !allowed[name]&&discardTrafficUser(state,name){changed=true}}
 for name:=range state.LastOnline {if !allowed[name]&&discardTrafficUser(state,name){changed=true}}
 for name:=range state.LastActive {if !allowed[name]&&discardTrafficUser(state,name){changed=true}}
 return changed,nil
}

func reconcileStoredTraffic(configPath string)(trafficStore,error){
 lock,err:=acquireConfigLock(configPath,8*time.Second);if err!=nil{return trafficStore{},err}
 defer releaseConfigLock(lock)
 state,err:=loadTrafficStore(configPath);if err!=nil{return state,err}
 changed,err:=reconcileTrafficUsersLocked(configPath,&state);if err!=nil{return state,err}
 if changed {err=saveTrafficStore(configPath,state)}
 return state,err
}

// Telegram /online explicitly queries live Xray rather than only reading
// the asynchronous 5-second collector snapshot.
func refreshOnlineTraffic(configPath,xrayBinary,statsServer string)(trafficStore,error) {
 lock,err:=acquireConfigLock(configPath,8*time.Second);if err!=nil{return trafficStore{},err}
 defer releaseConfigLock(lock)
 state,err:=loadTrafficStore(configPath);if err!=nil{return state,err}
 reconciled,err:=reconcileTrafficUsersLocked(configPath,&state);if err!=nil{return state,err}
 active,err:=activeTrafficUserSet(configPath);if err!=nil{return state,err}
 candidates:=make([]string,0,len(active))
 for name:=range active{candidates=append(candidates,name)}
 if groups,groupsErr:=managedClientEmailsFromConfig(configPath);groupsErr==nil {
  for name:=range active{candidates=append(candidates,groups[name]...)}
 }
 emails:=sortedUniqueStrings(candidates)
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
 online,queryErr:=queryOnlineClientsForEmails(ctx,xrayBinary,statsServer,emails)
 changed:=reconciled
 if queryErr==nil {
  filtered:=online[:0]
  for _,user:=range online{if active[bindingOwner(user.Email)]{filtered=append(filtered,user)}}
  if applyOnlineSnapshot(&state,filtered,time.Now()){changed=true}
 }
 if changed {if err=saveTrafficStore(configPath,state);err!=nil{return state,err}}
 return state,queryErr
}

func activeTrafficUserSet(configPath string)(map[string]bool,error){
 names,err:=allDirectoryNames(configPath);if err!=nil{return nil,err}
 allowed:=make(map[string]bool,len(names))
 for _,name:=range names{allowed[name]=true}
 return allowed,nil
}

func renameTrafficStoreLocked(configPath,oldName,newName string)error{
 if oldName==newName{return nil}
 state,err:=loadTrafficStore(configPath)
 if err!=nil{return err}
 if _,exists:=state.Users[newName];exists{return errors.New("The new name already has historical traffic")}
 type bindingMove struct{from,to string}
 moves:=make([]bindingMove,0)
 for email:=range state.Bindings {
  owner,tag,ok:=parseBindingEmail(email)
  if !ok||owner!=oldName{continue}
  target:=bindingEmail(newName,tag)
  if target==email{continue}
  if _,exists:=state.Bindings[target];exists{return fmt.Errorf("The new name already has traffic for inbound %s",tag)}
  moves=append(moves,bindingMove{from:email,to:target})
 }
 if item,ok:=state.Users[oldName];ok {
  item.LastUp=0
  item.LastDown=0
  item.Epoch=""
  state.Users[newName]=item
  delete(state.Users,oldName)
 }
 for _,move:=range moves {
  item:=state.Bindings[move.from]
  // The Xray client email changes, so its runtime counter starts at zero.
  // Keeping the old watermark would undercount until it catches up.
  item.LastUp=0
  item.LastDown=0
  item.Epoch=""
  state.Bindings[move.to]=item
  delete(state.Bindings,move.from)
 }
 if at,ok:=state.LastOnline[oldName];ok{state.LastOnline[newName]=at;delete(state.LastOnline,oldName)}
 if at,ok:=state.LastActive[oldName];ok{state.LastActive[newName]=at;delete(state.LastActive,oldName)}
 if ips,ok:=state.OnlineIPs[oldName];ok{state.OnlineIPs[newName]=ips;delete(state.OnlineIPs,oldName)}
 if ips,ok:=state.LiveIPs[oldName];ok{state.LiveIPs[newName]=ips;delete(state.LiveIPs,oldName)}
 return saveTrafficStore(configPath,state)
}
func renameTrafficStore(configPath,oldName,newName string)error{
 lock,err:=acquireConfigLock(configPath,5*time.Second)
 if err!=nil{return err}
 defer releaseConfigLock(lock)
 return renameTrafficStoreLocked(configPath,oldName,newName)
}

func resetTrafficStore(configPath string)error{
 return editTrafficStore(configPath,func(state *trafficStore)error{
  for name:=range state.Users {state.Users[name]=trafficCounter{}}
  for email:=range state.Bindings {state.Bindings[email]=trafficCounter{}}
  return nil
 })
}

func resetTrafficUserLocked(configPath,name string)error {
 state,err:=loadTrafficStore(configPath);if err!=nil{return err}
 state.Users[name]=trafficCounter{}
 for email:=range state.Bindings {
  if owner,_,ok:=parseBindingEmail(email);ok&&owner==name{state.Bindings[email]=trafficCounter{}}
 }
 return saveTrafficStore(configPath,state)
}

// resetUserTraffic clears the live Xray counters and then the persisted ledger
// entry for one user. Every binding email must be reset individually because
// statsquery matches patterns with strings.Contains and "user>>>alice>>>"
// never matches "user>>>alice+VEN...>>>".
func resetUserTraffic(configPath,xrayBinary,statsServer,email string)error{
 if !managedUserName.MatchString(email){return errors.New("Invalid user identifier")}
 lock,err:=acquireConfigLock(configPath,10*time.Second)
 if err!=nil{return err}
 defer releaseConfigLock(lock)
 state,err:=loadTrafficStore(configPath);if err!=nil{return err}
 aliases,err:=bindingEmailAliases(configPath,state,email)
 if err!=nil{return err}
 for _,alias:=range aliases {
  ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second)
  out,runErr:=exec.CommandContext(ctx,xrayBinary,"api","statsquery","--server="+statsServer,"-pattern=user>>>"+alias+">>>","-reset=true").CombinedOutput()
  cancel()
  if runErr!=nil{return fmt.Errorf("Xray counter reset failed for %s: %w: %s",alias,runErr,strings.TrimSpace(string(out)))}
 }
 return resetTrafficUserLocked(configPath,email)
}

func resetTrafficAllUsersLocked(configPath string)error {
 state,err:=loadTrafficStore(configPath);if err!=nil{return err}
 for name:=range state.Users{state.Users[name]=trafficCounter{}}
 for email:=range state.Bindings{state.Bindings[email]=trafficCounter{}}
 return saveTrafficStore(configPath,state)
}

// resetNodeBindingCounters keeps per-node history after an inbound is deleted
// but clears the runtime watermark, so a later node reusing the tag does not
// inherit a stale "last" value and undercount the new counter.
func resetNodeBindingCounters(configPath,tag string) error {
 return editTrafficStore(configPath,func(state *trafficStore)error{
  for email,item:=range state.Bindings {
   tagValue,ok:=bindingTag(email)
   if !ok||tagValue!=tag{continue}
   item.LastUp=0
   item.LastDown=0
   item.Epoch=""
   state.Bindings[email]=item
  }
  return nil
 })
}

type bindingTrafficEntry struct {
 Email string
 User string
 Tag string
 Uplink uint64
 Downlink uint64
}

func (e bindingTrafficEntry) Total() uint64 {return e.Uplink+e.Downlink}

func sortedBindingTraffic(entries []bindingTrafficEntry) []bindingTrafficEntry {
 sort.Slice(entries,func(i,j int)bool{
  if entries[i].Total()!=entries[j].Total(){return entries[i].Total()>entries[j].Total()}
  if entries[i].Tag!=entries[j].Tag{return entries[i].Tag<entries[j].Tag}
  return entries[i].User<entries[j].User
 })
 return entries
}

func storedBindingTrafficForUser(state trafficStore,user string) []bindingTrafficEntry {
 entries:=make([]bindingTrafficEntry,0)
 for email,item:=range state.Bindings {
  owner,tag,ok:=parseBindingEmail(email)
  if !ok||owner!=user{continue}
  entries=append(entries,bindingTrafficEntry{Email:email,User:owner,Tag:tag,Uplink:item.TotalUp,Downlink:item.TotalDown})
 }
 return sortedBindingTraffic(entries)
}

func storedBindingTrafficForTag(state trafficStore,tag string) []bindingTrafficEntry {
 entries:=make([]bindingTrafficEntry,0)
 for email,item:=range state.Bindings {
  owner,bindingTag,ok:=parseBindingEmail(email)
  if !ok||bindingTag!=tag{continue}
  entries=append(entries,bindingTrafficEntry{Email:email,User:owner,Tag:bindingTag,Uplink:item.TotalUp,Downlink:item.TotalDown})
 }
 return sortedBindingTraffic(entries)
}
