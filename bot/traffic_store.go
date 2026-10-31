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
 changed:=applyTrafficSnapshot(&state,users,inbounds,epoch)
 // Collect online IPs independently of traffic accounting. Unsupported
 // APIs must not stop the durable traffic sampler.
 if observed,onlineErr:=queryOnlineClients(ctx,xrayBinary,statsServer);onlineErr==nil {
  if applyOnlineSnapshot(&state,observed,time.Now()){changed=true}
 }else if !isOnlineUnsupported(onlineErr){
  log.Printf("[Online] optional poll failed: %v",onlineErr)
 }
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

func resetTrafficAllUsersLocked(configPath string)error {
 state,err:=loadTrafficStore(configPath);if err!=nil{return err}
 for name:=range state.Users{state.Users[name]=trafficCounter{}}
 return saveTrafficStore(configPath,state)
}
