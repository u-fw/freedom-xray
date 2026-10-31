package main

import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "log"
 "net"
 "os/exec"
 "reflect"
 "sort"
 "strconv"
 "strings"
 "sync"
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

// normalizeOnlineUsers validates addresses, sorts them, and drops users that
// have no usable IP. Both the aggregate and per-email response formats flow
// through this single normalization step.
func normalizeOnlineUsers(users []onlineUser, now int64) []onlineUser {
 result:=make([]onlineUser,0,len(users))
 for _,user:=range users {
  if user.Email==""{continue}
  entry:=onlineUser{Email:user.Email}
  for _,row:=range user.IPs {
   if net.ParseIP(row.IP)==nil{continue}
   lastSeen:=row.LastSeen
   if lastSeen<=0{lastSeen=now}
   entry.IPs=append(entry.IPs,onlineClientIP{IP:row.IP,LastSeen:lastSeen})
   if len(entry.IPs)>=32{break}
  }
  sort.Slice(entry.IPs,func(i,j int)bool{return entry.IPs[i].IP<entry.IPs[j].IP})
  if len(entry.IPs)>0 {result=append(result,entry)}
 }
 return result
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
  entry:=onlineUser{Email:user.Email}
  for _,row:=range user.IPs {
   entry.IPs=append(entry.IPs,onlineClientIP{IP:row.IP,LastSeen:parseOnlineTimestamp(row.LastSeen,now)})
  }
  users=append(users,entry)
 }
 return normalizeOnlineUsers(users,now),nil
}

// parseOnlineEmailResponse accepts both response shapes emitted by Xray CLI
// versions: the protobuf JSON form {"name":...,"ips":{"ip":lastSeen}} and an
// array-like form used by some builds.
func parseOnlineEmailResponse(out []byte, fallbackEmail string) (onlineUser,error) {
 var response struct {
  Name string `json:"name"`
  Email string `json:"email"`
  IPs json.RawMessage `json:"ips"`
 }
 if err:=json.Unmarshal(out,&response);err!=nil{return onlineUser{},err}
 email:=response.Email
 if email==""{email=response.Name}
 if email==""{email=fallbackEmail}
 result:=onlineUser{Email:email}
 now:=time.Now().Unix()
 raw:=bytes.TrimSpace(response.IPs)
 if len(raw)==0||bytes.Equal(raw,[]byte("null")){return result,nil}

 // Protobuf JSON map<string,int64>: {"203.0.113.7": 1700000000}.
 var mapped map[string]json.RawMessage
 if err:=json.Unmarshal(raw,&mapped);err==nil {
  for ip,seenRaw:=range mapped {
   result.IPs=append(result.IPs,onlineClientIP{IP:ip,LastSeen:parseOnlineTimestamp(seenRaw,now)})
  }
  return result,nil
 }

 // Array form: [{"ip":"203.0.113.7","lastSeen":"..."}].
 var listed []struct{
  IP string `json:"ip"`
  LastSeen json.RawMessage `json:"lastSeen"`
 }
 if err:=json.Unmarshal(raw,&listed);err!=nil {
  return onlineUser{},fmt.Errorf("unsupported ips field: %w",err)
 }
 for _,row:=range listed {
  result.IPs=append(result.IPs,onlineClientIP{IP:row.IP,LastSeen:parseOnlineTimestamp(row.LastSeen,now)})
 }
 return result,nil
}

// onlineAllFlagUnsupported matches cores that predate the GetUsersStats "-all"
// flag. Those cores still expose the long-standing per-email query.
func onlineAllFlagUnsupported(err error) bool {
 if err==nil{return false}
 text:=strings.ToLower(err.Error())
 for _,marker:=range []string{"flag provided but not defined","unknown flag","unknown shorthand flag","no such flag","unrecognized flag"} {
  if strings.Contains(text,marker){return true}
 }
 return false
}

// queryOnlineClientsByEmail is the compatibility path for Xray-core versions
// that implement statsonlineiplist but not its aggregate "-all" output.
func queryOnlineClientsByEmail(ctx context.Context, xrayBin,statsServer string, emails []string) ([]onlineUser,error) {
 if len(emails)==0{return nil,errors.New("no users available for per-email online statistics")}
 const workers=4
 semaphore:=make(chan struct{},workers)
 var wait sync.WaitGroup
 var mu sync.Mutex
 results:=make([]onlineUser,0,len(emails))
 failures:=make([]string,0)
 for _,email:=range emails {
  if ctx.Err()!=nil{break}
  wait.Add(1)
  semaphore<-struct{}{}
  go func(email string){
   defer wait.Done()
   defer func(){<-semaphore}()
   out,err:=exec.CommandContext(ctx,xrayBin,"api","statsonlineiplist","--server="+statsServer,"-email="+email).CombinedOutput()
   if err!=nil {
    mu.Lock();failures=append(failures,email+": "+strings.TrimSpace(string(out)));mu.Unlock()
    return
   }
   user,err:=parseOnlineEmailResponse(out,email)
   if err!=nil {
    mu.Lock();failures=append(failures,email+": "+err.Error());mu.Unlock()
    return
   }
   mu.Lock();results=append(results,user);mu.Unlock()
  }(email)
 }
 wait.Wait()
 if len(results)==0&&len(failures)>0 {
  return nil,errors.New("per-email online statistics failed: "+strings.Join(failures,"; "))
 }
 if len(failures)>0 {
  log.Printf("[Online] %d per-email online queries failed (first: %s)",len(failures),failures[0])
 }
 return normalizeOnlineUsers(results,time.Now().Unix()),nil
}

// Some core builds accept "-all" but return an empty user list even though the
// per-email path has data. Probe that compatibility path occasionally, then
// prefer it for a bounded period. The cache is scoped to the binary path so a
// core upgrade is detected immediately.
var onlineFallbackCache=struct{
 sync.Mutex
 bin string
 active bool
 until time.Time
 probeAfter time.Time
}{}

func onlineFallbackPlan(bin string,emailCount int,now time.Time)(usePerEmail,allowProbe bool){
 onlineFallbackCache.Lock()
 defer onlineFallbackCache.Unlock()
 if onlineFallbackCache.bin!=bin {
  onlineFallbackCache.bin=bin
  onlineFallbackCache.active=false
  onlineFallbackCache.until=time.Time{}
  onlineFallbackCache.probeAfter=time.Time{}
 }
 if emailCount<1||emailCount>32{return false,false}
 usePerEmail=onlineFallbackCache.active&&now.Before(onlineFallbackCache.until)
 allowProbe=!usePerEmail&&!now.Before(onlineFallbackCache.probeAfter)
 return usePerEmail,allowProbe
}

func recordOnlineFallback(bin string,found bool,now time.Time){
 onlineFallbackCache.Lock()
 defer onlineFallbackCache.Unlock()
 if onlineFallbackCache.bin!=bin {
  onlineFallbackCache.bin=bin
  onlineFallbackCache.active=false
  onlineFallbackCache.until=time.Time{}
  onlineFallbackCache.probeAfter=time.Time{}
 }
 if found {
  onlineFallbackCache.active=true
  onlineFallbackCache.until=now.Add(10*time.Minute)
  onlineFallbackCache.probeAfter=time.Time{}
  return
 }
 onlineFallbackCache.active=false
 onlineFallbackCache.probeAfter=now.Add(2*time.Minute)
}

// queryOnlineClientsForEmails prefers the single aggregate core query. When an
// older core rejects "-all", or accepts it but never reports users, it falls
// back to bounded per-email queries so online-IP collection keeps working
// without forcing a core upgrade.
func queryOnlineClientsForEmails(ctx context.Context, xrayBin,statsServer string, emails []string) ([]onlineUser,error) {
 now:=time.Now()
 usePerEmail,allowProbe:=onlineFallbackPlan(xrayBin,len(emails),now)
 if usePerEmail {
  users,err:=queryOnlineClientsByEmail(ctx,xrayBin,statsServer,emails)
  if err==nil{return users,nil}
  recordOnlineFallback(xrayBin,false,time.Now())
 }
 users,err:=queryOnlineClients(ctx,xrayBin,statsServer)
 if err==nil {
  if len(users)==0&&allowProbe {
   fallback,fallbackErr:=queryOnlineClientsByEmail(ctx,xrayBin,statsServer,emails)
   if fallbackErr==nil&&len(fallback)>0 {
    recordOnlineFallback(xrayBin,true,time.Now())
    return fallback,nil
   }
   recordOnlineFallback(xrayBin,false,time.Now())
  }
  return users,nil
 }
 if !onlineAllFlagUnsupported(err) {return nil,err}
 return queryOnlineClientsByEmail(ctx,xrayBin,statsServer,emails)
}

func isOnlineUnsupported(err error) bool {
 if err==nil{return false}
 text:=strings.ToLower(err.Error())
 return errors.Is(err,exec.ErrNotFound)||strings.Contains(text,"unknown command")||strings.Contains(text,"unimplemented")||strings.Contains(text,"not implemented")||onlineAllFlagUnsupported(err)
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
