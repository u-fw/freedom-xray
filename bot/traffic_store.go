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
 changed:=false
 for _,u:=range users {
  if u.Email==""{continue}
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
 seen:=make(map[string]bool,len(state.Users))
 for name:=range state.Users{seen[name]=true}
 if names,err:=allDirectoryNames(configPath);err==nil {
  for _,name:=range names{seen[name]=true}
 }
 emails:=make([]string,0,len(seen))
 for name:=range seen{emails=append(emails,name)}
 sort.Strings(emails)
 return emails
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
 if _,ok:=state.Users[name];ok{delete(state.Users,name);changed=true}
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
 emails:=make([]string,0,len(active))
 for name:=range active{emails=append(emails,name)}
 sort.Strings(emails)
 ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
 online,queryErr:=queryOnlineClientsForEmails(ctx,xrayBinary,statsServer,emails)
 changed:=reconciled
 if queryErr==nil {
  filtered:=online[:0]
  for _,user:=range online{if active[user.Email]{filtered=append(filtered,user)}}
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
 state,err:=loadTrafficStore(configPath)
 if err!=nil{return err}
 item,ok:=state.Users[oldName];if !ok{return nil}
 if _,exists:=state.Users[newName];exists{return errors.New("The new name already has historical traffic")}
 item.LastUp=0
 item.LastDown=0
 item.Epoch=""
 state.Users[newName]=item
 delete(state.Users,oldName)
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
  return nil
 })
}

func resetTrafficUserLocked(configPath,name string)error {
 state,err:=loadTrafficStore(configPath);if err!=nil{return err}
 state.Users[name]=trafficCounter{}
 return saveTrafficStore(configPath,state)
}

// resetUserTraffic clears the live Xray counter and then the persisted ledger
// entry for one user. The runtime reset must succeed first: zeroing only the
// ledger would add the still-live counter back on the next sample.
func resetUserTraffic(configPath,xrayBinary,statsServer,email string)error{
 if !managedUserName.MatchString(email){return errors.New("Invalid user identifier")}
 lock,err:=acquireConfigLock(configPath,10*time.Second)
 if err!=nil{return err}
 defer releaseConfigLock(lock)
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second)
 defer cancel()
 out,err:=exec.CommandContext(ctx,xrayBinary,"api","statsquery","--server="+statsServer,"-pattern=user>>>"+email+">>>","-reset=true").CombinedOutput()
 if err!=nil{return fmt.Errorf("Xray counter reset failed: %w: %s",err,strings.TrimSpace(string(out)))}
 return resetTrafficUserLocked(configPath,email)
}

func resetTrafficAllUsersLocked(configPath string)error {
 state,err:=loadTrafficStore(configPath);if err!=nil{return err}
 for name:=range state.Users{state.Users[name]=trafficCounter{}}
 return saveTrafficStore(configPath,state)
}
