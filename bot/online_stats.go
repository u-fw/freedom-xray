package main

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "net"
 "os/exec"
 "reflect"
 "sort"
 "strconv"
 "strings"
 "time"
)

type onlineClientIP struct {
 IP string `json:"ip"`
 LastSeen int64 `json:"last_seen"`
}
type onlineUser struct {
 Email string
 IPs []onlineClientIP
}

func parseOnlineTimestamp(raw json.RawMessage, fallback int64) int64 {
 if len(raw)==0{return fallback}
 var text string
 if err:=json.Unmarshal(raw,&text);err!=nil {
  var num json.Number
  if json.Unmarshal(raw,&num)!=nil{return fallback}
  text=string(num)
 }
 seconds,err:=strconv.ParseInt(text,10,64)
 if err!=nil||seconds<=0{return fallback}
 if seconds>100000000000 {seconds/=1000}
 if seconds>fallback+60{return fallback}
 return seconds
}

// Xray-core implements GetUsersStats through the statsonlineiplist CLI.
// Querying the core directly avoids parsing connection/access logs.
func queryOnlineClients(ctx context.Context, xrayBin,statsServer string) ([]onlineUser,error) {
 out,err:=exec.CommandContext(ctx,xrayBin,"api","statsonlineiplist","--server="+statsServer,"-all").CombinedOutput()
 if err!=nil{return nil,fmt.Errorf("online stats query failed: %w: %s",err,strings.TrimSpace(string(out)))}
 var response struct {
  Users []struct{
   Email string `json:"email"`
   IPs []struct{
    IP string `json:"ip"`
    LastSeen json.RawMessage `json:"lastSeen"`
   } `json:"ips"`
  } `json:"users"`
 }
 if err=json.Unmarshal(out,&response);err!=nil{return nil,fmt.Errorf("online stats malformed: %w",err)}
 now:=time.Now().Unix()
 users:=make([]onlineUser,0,len(response.Users))
 for _,user:=range response.Users {
  if user.Email==""{continue}
  result:=onlineUser{Email:user.Email}
  for _,row:=range user.IPs {
   if net.ParseIP(row.IP)==nil{continue}
   result.IPs=append(result.IPs,onlineClientIP{IP:row.IP,LastSeen:parseOnlineTimestamp(row.LastSeen,now)})
   if len(result.IPs)>=32{break}
  }
  sort.Slice(result.IPs,func(i,j int)bool{return result.IPs[i].IP<result.IPs[j].IP})
  if len(result.IPs)>0 {users=append(users,result)}
 }
 return users,nil
}
func isOnlineUnsupported(err error) bool {
 if err==nil{return false}
 text:=strings.ToLower(err.Error())
 return errors.Is(err,exec.ErrNotFound)||strings.Contains(text,"unknown command")||strings.Contains(text,"unimplemented")||strings.Contains(text,"not implemented")
}
func applyOnlineSnapshot(state *trafficStore,observed []onlineUser,now time.Time) bool {
 ensureTrafficMaps(state)
 current:=make(map[string][]onlineClientIP)
 changed:=false
 t:=now.Unix()
 for _,u:=range observed {
  if u.Email==""||len(u.IPs)==0{continue}
  current[u.Email]=u.IPs
  // The user is connected now, even when the core's most recent dispatch
  // time is older (idle long-lived connections).
  if t>state.LastOnline[u.Email] {state.LastOnline[u.Email]=t;changed=true}
  history:=make(map[string]int64)
  for _,ip:=range state.OnlineIPs[u.Email] {history[ip.IP]=ip.LastSeen}
  for _,ip:=range u.IPs {
   if ip.LastSeen>history[ip.IP] {history[ip.IP]=ip.LastSeen;changed=true}
  }
  all:=make([]onlineClientIP,0,len(history))
  for ip,seen:=range history {all=append(all,onlineClientIP{IP:ip,LastSeen:seen})}
  sort.Slice(all,func(i,j int)bool{return all[i].LastSeen>all[j].LastSeen})
  if len(all)>32 {all=all[:32];changed=true}
  state.OnlineIPs[u.Email]=all
 }
 if !reflect.DeepEqual(state.LiveIPs,current){state.LiveIPs=current;changed=true}
 if state.OnlinePollAt!=t {state.OnlinePollAt=t;changed=true}
 if pruneObservedIPs(state,now){changed=true}
 return changed
}
func recentOnline(state trafficStore,email string,now time.Time) (ips []onlineClientIP,known bool) {
 if state.OnlinePollAt<=0||now.Unix()-state.OnlinePollAt>20||state.OnlinePollAt>now.Unix()+5{return nil,false}
 return state.LiveIPs[email],true
}
