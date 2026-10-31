package main

import (
 "bytes"
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

// Xray removes an address from its online map as soon as the last session
// closes, so a REALITY client that is idle between requests can be invisible
// at poll time. The Bot therefore keeps its own observation history. The map
// per user is bounded; both limits also bound the SQLite rewrite size.
const maxOnlineIPsPerUser=64
const onlineHistoryRetention=7*24*time.Hour
const maxOnlineProbeEmails=128
// Traffic seen within this window is treated as a live session for protocols
// whose connections close between polls (REALITY).
const onlineActivityWindow=60*time.Second

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

// parseOnlineIPs accepts both shapes emitted by Xray builds: the aggregate
// array [{"ip":"...","lastSeen":...}] and the per-email protobuf map
// {"203.0.113.7": 1700000000}. Timestamps may be decimal strings or numbers.
func parseOnlineIPs(raw json.RawMessage, fallback int64) ([]onlineClientIP,error) {
 raw=bytes.TrimSpace(raw)
 if len(raw)==0||bytes.Equal(raw,[]byte("null")){return nil,nil}
 var listed []struct{
  IP string `json:"ip"`
  LastSeen json.RawMessage `json:"lastSeen"`
 }
 if err:=json.Unmarshal(raw,&listed);err==nil {
  ips:=make([]onlineClientIP,0,len(listed))
  for _,row:=range listed {
   ips=append(ips,onlineClientIP{IP:row.IP,LastSeen:parseOnlineTimestamp(row.LastSeen,fallback)})
  }
  return ips,nil
 }
 var mapped map[string]json.RawMessage
 if err:=json.Unmarshal(raw,&mapped);err==nil {
  ips:=make([]onlineClientIP,0,len(mapped))
  for ip,seenRaw:=range mapped {
   ips=append(ips,onlineClientIP{IP:ip,LastSeen:parseOnlineTimestamp(seenRaw,fallback)})
  }
  return ips,nil
 }
 return nil,errors.New("unsupported ips field")
}

// parseStatInt reads a stat value that protobuf JSON renders as a decimal
// string while some builds render as a bare number.
func parseStatInt(raw json.RawMessage) (int64,bool) {
 if len(raw)==0{return 0,false}
 var text string
 if json.Unmarshal(raw,&text)==nil {
  value,err:=strconv.ParseInt(text,10,64)
  return value,err==nil
 }
 var number json.Number
 if json.Unmarshal(raw,&number)==nil {
  value,err:=strconv.ParseInt(number.String(),10,64)
  return value,err==nil
 }
 return 0,false
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
   if len(entry.IPs)>=maxOnlineIPsPerUser{break}
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
   IPs json.RawMessage `json:"ips"`
  } `json:"users"`
 }
 if err=json.Unmarshal(out,&response);err!=nil{return nil,fmt.Errorf("online stats malformed: %w",err)}
 now:=time.Now().Unix()
 users:=make([]onlineUser,0,len(response.Users))
 malformed:=0
 for _,user:=range response.Users {
  ips,parseErr:=parseOnlineIPs(user.IPs,now)
  if parseErr!=nil{malformed++;continue}
  users=append(users,onlineUser{Email:user.Email,IPs:ips})
 }
 if malformed>0 {
  return nil,fmt.Errorf("online stats malformed: %d user entries had an unsupported ips field",malformed)
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
 ips,err:=parseOnlineIPs(raw,now)
 if err!=nil{return onlineUser{},fmt.Errorf("unsupported ips field: %w",err)}
 result.IPs=ips
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
 const workers=8
 semaphore:=make(chan struct{},workers)
 var wait sync.WaitGroup
 var mu sync.Mutex
 results:=make([]onlineUser,0,len(emails))
 failures:=make([]string,0)
 for _,email:=range emails {
  select {
  case <-ctx.Done():
   break
  case semaphore<-struct{}{}:
  }
  if ctx.Err()!=nil {break}
  wait.Add(1)
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
 results=normalizeOnlineUsers(results,time.Now().Unix())
 if ctx.Err()!=nil{return results,fmt.Errorf("per-email online statistics incomplete: %w",ctx.Err())}
 if len(failures)>0 {
  return results,fmt.Errorf("%d per-email online queries failed (first: %s)",len(failures),failures[0])
 }
 return results,nil
}

// queryOnlineSessionCount reads the "user>>>email>>>online" counter. It
// separates "connected but no address recorded" (for example when a local
// relay makes Xray see 127.0.0.1, which its online map drops) from "not
// connected at all" during diagnostics.
func queryOnlineSessionCount(ctx context.Context, xrayBin,statsServer,email string) (int64,error) {
 out,err:=exec.CommandContext(ctx,xrayBin,"api","statsonline","--server="+statsServer,"-email="+email).CombinedOutput()
 if err!=nil {
  return 0,fmt.Errorf("online session query failed: %w: %s",err,strings.TrimSpace(string(out)))
 }
 var response struct {
  Stat struct {
   Value json.RawMessage `json:"value"`
  } `json:"stat"`
 }
 if err=json.Unmarshal(out,&response);err!=nil{return 0,fmt.Errorf("online session response malformed: %w",err)}
 value,ok:=parseStatInt(response.Stat.Value)
 if !ok{return 0,errors.New("online session response has no numeric value")}
 return value,nil
}

func queryOnlineSessionCounts(ctx context.Context,xrayBin,statsServer string,emails []string) map[string]int64 {
 counts:=make(map[string]int64,len(emails))
 const workers=4
 semaphore:=make(chan struct{},workers)
 var wait sync.WaitGroup
 var mu sync.Mutex
 for _,email:=range emails {
  if ctx.Err()!=nil{break}
  wait.Add(1)
  semaphore<-struct{}{}
  go func(email string){
   defer wait.Done()
   defer func(){<-semaphore}()
   count,err:=queryOnlineSessionCount(ctx,xrayBin,statsServer,email)
   if err!=nil{return}
   mu.Lock();counts[email]=count;mu.Unlock()
  }(email)
 }
 wait.Wait()
 return counts
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
 failure string
}{}

func onlineFallbackPlan(bin string,emailCount int,now time.Time)(usePerEmail,allowProbe bool){
 onlineFallbackCache.Lock()
 defer onlineFallbackCache.Unlock()
 if onlineFallbackCache.bin!=bin {
  onlineFallbackCache.bin=bin
  onlineFallbackCache.active=false
  onlineFallbackCache.until=time.Time{}
  onlineFallbackCache.probeAfter=time.Time{}
  onlineFallbackCache.failure=""
 }
 if emailCount<1||emailCount>maxOnlineProbeEmails{return false,false}
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
  onlineFallbackCache.failure=""
 }
 onlineFallbackCache.failure=""
 if found {
  onlineFallbackCache.active=true
  onlineFallbackCache.until=now.Add(10*time.Minute)
  onlineFallbackCache.probeAfter=time.Time{}
  return
 }
 onlineFallbackCache.active=false
 onlineFallbackCache.probeAfter=now.Add(2*time.Minute)
}

func recordOnlineFallbackFailure(bin string,err error,now time.Time) {
 onlineFallbackCache.Lock()
 defer onlineFallbackCache.Unlock()
 onlineFallbackCache.bin=bin
 onlineFallbackCache.active=false
 onlineFallbackCache.until=time.Time{}
 onlineFallbackCache.probeAfter=now.Add(2*time.Minute)
 onlineFallbackCache.failure=err.Error()
}

func cachedOnlineFallbackFailure(bin string,now time.Time) error {
 onlineFallbackCache.Lock()
 defer onlineFallbackCache.Unlock()
 if onlineFallbackCache.bin!=bin||onlineFallbackCache.failure==""||!now.Before(onlineFallbackCache.probeAfter){return nil}
 return errors.New(onlineFallbackCache.failure)
}

// queryOnlineClientsForEmails prefers the single aggregate core query. When an
// older core rejects "-all", accepts it but never reports users, or fails the
// aggregate RPC while the per-email RPC still works, it falls back to bounded
// per-email queries so online-IP collection keeps working without forcing a
// core upgrade.
func queryOnlineClientsForEmails(ctx context.Context, xrayBin,statsServer string, emails []string) ([]onlineUser,error) {
 now:=time.Now()
 usePerEmail,allowProbe:=onlineFallbackPlan(xrayBin,len(emails),now)
 triedPerEmail:=false
 var perEmailErr error
 if usePerEmail {
  triedPerEmail=true
  users,err:=queryOnlineClientsByEmail(ctx,xrayBin,statsServer,emails)
  if err==nil {
   if len(users)>0{return users,nil}
   // A cached preference must not hide data that the aggregate endpoint
   // started returning after a core upgrade.
   if aggregate,aggErr:=queryOnlineClients(ctx,xrayBin,statsServer);aggErr==nil&&len(aggregate)>0 {
    return aggregate,nil
   }
   return users,nil
  }
  perEmailErr=err
  recordOnlineFallbackFailure(xrayBin,err,time.Now())
 }
 users,err:=queryOnlineClients(ctx,xrayBin,statsServer)
 if err==nil {
  if len(users)==0&&perEmailErr!=nil{return nil,fmt.Errorf("online fallback incomplete: %w",perEmailErr)}
  if len(users)==0&&!allowProbe {
   if failure:=cachedOnlineFallbackFailure(xrayBin,time.Now());failure!=nil{return nil,fmt.Errorf("online fallback incomplete: %w",failure)}
  }
  if len(users)==0&&allowProbe {
   fallback,fallbackErr:=queryOnlineClientsByEmail(ctx,xrayBin,statsServer,emails)
   if fallbackErr==nil&&len(fallback)>0 {
    recordOnlineFallback(xrayBin,true,time.Now())
    return fallback,nil
   }
   if fallbackErr!=nil {
    recordOnlineFallbackFailure(xrayBin,fallbackErr,time.Now())
    return nil,fmt.Errorf("online fallback incomplete: %w",fallbackErr)
   }
   recordOnlineFallback(xrayBin,false,time.Now())
  }
  return users,nil
 }
 // A missing aggregate RPC, a CLI that predates the command, or a malformed
 // aggregate response must not hide a working per-email endpoint. Keep the
 // probe on the same bounded schedule so a down core is not hammered.
 if !triedPerEmail&&allowProbe {
  fallback,fallbackErr:=queryOnlineClientsByEmail(ctx,xrayBin,statsServer,emails)
  if fallbackErr==nil&&len(fallback)>0 {
   recordOnlineFallback(xrayBin,true,time.Now())
   return fallback,nil
  }
  if fallbackErr!=nil {recordOnlineFallbackFailure(xrayBin,fallbackErr,time.Now())}else{recordOnlineFallback(xrayBin,false,time.Now())}
 }
 return nil,err
}

func isOnlineUnsupported(err error) bool {
 if err==nil{return false}
 text:=strings.ToLower(err.Error())
 return errors.Is(err,exec.ErrNotFound)||strings.Contains(text,"unknown command")||strings.Contains(text,"unimplemented")||strings.Contains(text,"not implemented")||onlineAllFlagUnsupported(err)
}
func applyOnlineSnapshot(state *trafficStore,observed []onlineUser,now time.Time) bool {
 ensureTrafficMaps(state)
 // Xray reports one entry per client email. A user usually owns several
 // binding emails, so aggregate them into one owner entry before the card,
 // history, or LastOnline maps see the data.
 observedByOwner:=make(map[string]map[string]int64)
 changed:=false
 t:=now.Unix()
 for _,u:=range observed {
  if u.Email==""||len(u.IPs)==0{continue}
  owner:=bindingOwner(u.Email)
  if owner==""{continue}
  if observedByOwner[owner]==nil{observedByOwner[owner]=make(map[string]int64)}
  for _,ip:=range u.IPs {
   if ip.LastSeen>observedByOwner[owner][ip.IP]{observedByOwner[owner][ip.IP]=ip.LastSeen}
  }
  // The user is connected now, even when the core's most recent dispatch
  // time is older (idle long-lived connections).
  if t>state.LastOnline[owner]{state.LastOnline[owner]=t;changed=true}
 }
 current:=make(map[string][]onlineClientIP,len(observedByOwner))
 for owner,seen:=range observedByOwner {
  live:=make([]onlineClientIP,0,len(seen))
  for ip,lastSeen:=range seen{live=append(live,onlineClientIP{IP:ip,LastSeen:lastSeen})}
  sort.Slice(live,func(i,j int)bool{
   if live[i].LastSeen!=live[j].LastSeen{return live[i].LastSeen>live[j].LastSeen}
   return live[i].IP<live[j].IP
  })
  if len(live)>maxOnlineIPsPerUser{live=live[:maxOnlineIPsPerUser]}
  current[owner]=live
  history:=make(map[string]int64)
  for _,ip:=range state.OnlineIPs[owner]{history[ip.IP]=ip.LastSeen}
  for ip,lastSeen:=range seen {
   if lastSeen>history[ip]{history[ip]=lastSeen;changed=true}
  }
  all:=make([]onlineClientIP,0,len(history))
  for ip,lastSeen:=range history{all=append(all,onlineClientIP{IP:ip,LastSeen:lastSeen})}
  sort.Slice(all,func(i,j int)bool{return all[i].LastSeen>all[j].LastSeen})
  if len(all)>maxOnlineIPsPerUser{all=all[:maxOnlineIPsPerUser];changed=true}
  state.OnlineIPs[owner]=all
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

// REALITY clients open short-lived TLS connections that frequently close
// between two sampler ticks, so the core's live online map can be empty even
// while the user is actively transferring. A counter increase proves activity
// inside the sample window, so record it as a last-online observation even
// when no source address could be captured.
func inferOnlineFromTraffic(state *trafficStore,previous map[string]trafficCounter,now time.Time) bool {
 ensureTrafficMaps(state)
 changed:=false
 stamp:=now.Unix()
 for email,current:=range state.Users {
  old:=previous[email]
  if current.TotalUp+current.TotalDown<=old.TotalUp+old.TotalDown {continue}
  if stamp>state.LastActive[email] {state.LastActive[email]=stamp;changed=true}
  if stamp>state.LastOnline[email] {state.LastOnline[email]=stamp;changed=true}
 }
 return changed
}
