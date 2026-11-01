package main

import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "os"
 "os/exec"
 "sort"
 "strconv"
 "strings"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// onlineLevelInfo is the read-only view of one policy level that participates
// in online statistics. Clients counts users actually assigned to the level.
type onlineLevelInfo struct {
 Level string `json:"level"`
 Enabled bool `json:"enabled"`
 TrafficEnabled bool `json:"traffic_enabled"`
 Clients int `json:"clients"`
}

func clientLevelString(value any) string {
 switch v:=value.(type) {
 case float64:
  return strconv.Itoa(int(v))
 case json.Number:
  return v.String()
 case string:
  if v!="" {return v}
 }
 return "0"
}

func lessLevelName(a,b string) bool {
 ai,aerr:=strconv.Atoi(a);bi,berr:=strconv.Atoi(b)
 if aerr==nil&&berr==nil{return ai<bi}
 if aerr==nil{return true}
 if berr==nil{return false}
 return a<b
}

// inspectOnlinePolicy reports whether statsUserOnline is enabled for level 0
// and for every level actually referenced by an inbound client. This is the
// first thing to check when the online API returns an empty user list.
func inspectOnlinePolicy(configPath string) ([]onlineLevelInfo,error) {
 data,err:=os.ReadFile(configPath)
 if err!=nil{return nil,err}
 var root struct {
  Policy struct {
   Levels map[string]map[string]any `json:"levels"`
  } `json:"policy"`
  Inbounds []map[string]any `json:"inbounds"`
 }
 if err=json.Unmarshal(data,&root);err!=nil{return nil,err}

 clients:=map[string]int{}
 for _,in:=range root.Inbounds {
  if in["tag"]=="api-internal"{continue}
  list,_:=clientList(in)
  for _,raw:=range list {
   client,_:=getMap(raw)
   clients[clientLevelString(client["level"])]++
  }
 }
 names:=map[string]bool{"0":true}
 for level:=range clients{names[level]=true}
 for level:=range root.Policy.Levels{names[level]=true}
 ordered:=make([]string,0,len(names))
 for level:=range names{ordered=append(ordered,level)}
 sort.Slice(ordered,func(i,j int)bool{return lessLevelName(ordered[i],ordered[j])})

 result:=make([]onlineLevelInfo,0,len(ordered))
 for _,level:=range ordered {
  enabled,trafficEnabled:=false,false
  if settings,ok:=root.Policy.Levels[level];ok {
   enabled=settings["statsUserOnline"]==true
   trafficEnabled=settings["statsUserUplink"]==true&&settings["statsUserDownlink"]==true
  }
  result=append(result,onlineLevelInfo{Level:level,Enabled:enabled,TrafficEnabled:trafficEnabled,Clients:clients[level]})
 }
 return result,nil
}

// missingOnlineLevels returns every policy level that has clients but does not
// enable online statistics. Such levels cannot report source IPs at all, so
// the online card warns instead of silently showing empty addresses.
func missingOnlineLevels(configPath string) ([]string,error) {
 levels,err:=inspectOnlinePolicy(configPath)
 if err!=nil{return nil,err}
 missing:=make([]string,0)
 for _,item:=range levels {
  if item.Clients>0&&!item.Enabled {missing=append(missing,item.Level)}
 }
 return missing,nil
}

func onlineCoreVersion(ctx context.Context,bin string) string {
 out,err:=exec.CommandContext(ctx,bin,"version").CombinedOutput()
 text:=strings.TrimSpace(string(out))
 if err!=nil {
  if text==""{text=err.Error()}
  return "error: "+truncateLabel(text,180)
 }
 for _,line:=range strings.Split(text,"\n") {
  if strings.TrimSpace(line)!=""{return truncateLabel(strings.TrimSpace(line),180)}
 }
 return "unknown"
}

func onlineAggregateRaw(ctx context.Context,bin,statsServer string) (string,error) {
 out,err:=exec.CommandContext(ctx,bin,"api","statsonlineiplist","--server="+statsServer,"-all").CombinedOutput()
 text:=strings.TrimSpace(string(out))
 if err!=nil {
  if text==""{return "",err}
  return "",fmt.Errorf("%w: %s",err,text)
 }
 var compact bytes.Buffer
 if json.Compact(&compact,out)==nil{text=compact.String()}
 return truncateLabel(text,900),nil
}

func onlineAggregateEmpty(raw string) bool {
 compact:=strings.ReplaceAll(raw," ","")
 return raw==""||strings.Contains(compact,`"users":[]`)||strings.Contains(compact,`"users":null`)
}

// onlineProbeCandidates prefers users with recorded activity so the probe
// checks the addresses an administrator most likely cares about.
func onlineProbeCandidates(configPath string) []string {
 state,err:=loadTrafficStore(configPath)
 if err==nil&&len(state.Users)>0 {
  names:=make([]string,0,len(state.Users))
  for name:=range state.Users{names=append(names,name)}
  sort.Slice(names,func(i,j int)bool{
   a,b:=state.LastOnline[names[i]],state.LastOnline[names[j]]
   if a!=b{return a>b}
   return names[i]<names[j]
  })
  return names
 }
 names,_:=allDirectoryNames(configPath)
 return names
}

// onlineProbeText exercises the per-email endpoint for a small sample of
// managed users. It separates "no active session" from "session exists but no
// address was recorded", which is what a local relay or a client that is idle
// between requests looks like from Xray's point of view.
func (s *BotService) onlineProbeText() string {
 lang:=s.cfg.Language
 names:=onlineProbeCandidates(s.cfg.XrayConfigPath)
 if len(names)==0{return ""}
 limit:=len(names);if limit>4{limit=4}
 sample:=names[:limit]
 groups,groupErr:=managedClientEmailsFromConfig(s.cfg.XrayConfigPath)
 probeEmails:=make([]string,0,len(sample))
 for _,name:=range sample {
  emails:=groups[name]
  if groupErr!=nil||len(emails)==0{emails=[]string{name}}
  for _,email:=range emails {
   if len(probeEmails)>=maxOnlineProbeEmails{break}
   probeEmails=append(probeEmails,email)
  }
  probeEmails=sortedUniqueStrings(probeEmails)
 }
 ipCtx,ipCancel:=context.WithTimeout(context.Background(),8*time.Second)
 users,probeErr:=queryOnlineClientsByEmail(ipCtx,s.cfg.XrayBinPath,s.cfg.StatsAPIServer,probeEmails)
 ipCancel()
 countCtx,countCancel:=context.WithTimeout(context.Background(),8*time.Second)
 counts:=queryOnlineSessionCounts(countCtx,s.cfg.XrayBinPath,s.cfg.StatsAPIServer,probeEmails)
 countCancel()
 ipSet:=map[string]map[string]bool{}
 for _,user:=range users{
  owner:=bindingOwner(user.Email)
  if ipSet[owner]==nil{ipSet[owner]=map[string]bool{}}
  for _,ip:=range user.IPs{ipSet[owner][ip.IP]=true}
 }
 sessionCount:=map[string]int64{}
 counted:=map[string]bool{}
 for _,email:=range probeEmails {
  count,ok:=counts[email]
  if !ok{continue}
  counted[bindingOwner(email)]=true
  sessionCount[bindingOwner(email)]+=count
 }
 var out strings.Builder
 out.WriteString(tr(lang,"online.doctor.probe"))
 if probeErr!=nil&&len(users)==0 {
  out.WriteString(tr(lang,"online.doctor.probeerror",mdEsc(truncateLabel(probeErr.Error(),300))))
  return out.String()
 }
 if probeErr!=nil {
  out.WriteString(tr(lang,"online.doctor.probewarn",mdEsc(truncateLabel(probeErr.Error(),300))))
 }
 sawSessionsWithoutIP:=false
 for _,name:=range sample {
  sessions:="?"
  if counted[name] {
   count:=sessionCount[name]
   sessions=strconv.FormatInt(count,10)
   if count>0&&len(ipSet[name])==0{sawSessionsWithoutIP=true}
  }
  out.WriteString(tr(lang,"online.doctor.proberow",mdCode(name),sessions,len(ipSet[name])))
 }
 if len(names)>limit{out.WriteString(tr(lang,"online.doctor.probemore",len(names)-limit))}
 if sawSessionsWithoutIP{out.WriteString(tr(lang,"online.doctor.hint.sessions"))}
 return out.String()
}

func (s *BotService) onlineDiagnosticText() string {
 lang:=s.cfg.Language
 var out strings.Builder
 out.WriteString(tr(lang,"online.doctor.title"))
 out.WriteString(tr(lang,"online.doctor.bot",mdCode(Version)))

 coreCtx,coreCancel:=context.WithTimeout(context.Background(),5*time.Second)
 core:=onlineCoreVersion(coreCtx,s.cfg.XrayBinPath)
 coreCancel()
 out.WriteString(tr(lang,"online.doctor.core",mdCode(core)))

 out.WriteString(tr(lang,"online.doctor.policy"))
 levels,levelErr:=inspectOnlinePolicy(s.cfg.XrayConfigPath)
 missing:=[]string{}
 if levelErr!=nil {
  out.WriteString(tr(lang,"online.doctor.policyerror",mdEsc(levelErr.Error())))
 }else{
  limit:=len(levels);if limit>12{limit=12}
  for _,item:=range levels[:limit] {
   state:=tr(lang,"online.doctor.off");if item.Enabled{state=tr(lang,"online.doctor.on")}
   out.WriteString(tr(lang,"online.doctor.level",mdCode(item.Level),state,item.Clients))
   if item.Clients>0&&!item.Enabled{missing=append(missing,item.Level)}
  }
  if len(levels)>limit{out.WriteString(tr(lang,"online.doctor.levels.truncated",len(levels)-limit))}
 }
 if len(missing)>0{out.WriteString(tr(lang,"online.doctor.missing",mdCode(strings.Join(missing,", "))))}

 out.WriteString(tr(lang,"online.doctor.api"))
 apiCtx,apiCancel:=context.WithTimeout(context.Background(),5*time.Second)
 raw,apiErr:=onlineAggregateRaw(apiCtx,s.cfg.XrayBinPath,s.cfg.StatsAPIServer)
 apiCancel()
 if apiErr!=nil {
  out.WriteString(tr(lang,"online.doctor.apifail",mdEsc(truncateLabel(apiErr.Error(),300))))
 }else{
  out.WriteString(tr(lang,"online.doctor.apiok",mdCode(raw)))
 }
 out.WriteString(s.onlineProbeText())

 if store,err:=loadTrafficStore(s.cfg.XrayConfigPath);err==nil {
  live,recent:=0,0
  for _,ips:=range store.LiveIPs{if len(ips)>0{live++}}
  for _,ips:=range store.OnlineIPs{if len(ips)>0{recent++}}
  polled:="never"
  if store.OnlinePollAt>0{polled=time.Unix(store.OnlinePollAt,0).Format("2006-01-02 15:04:05")}
  out.WriteString(tr(lang,"online.doctor.store",live,recent,mdCode(polled)))
 }

 switch {
 case len(missing)>0:
  out.WriteString(tr(lang,"online.doctor.hint.missing"))
 case apiErr!=nil:
  out.WriteString(tr(lang,"online.doctor.hint.apifail"))
 case onlineAggregateEmpty(raw):
  out.WriteString(tr(lang,"online.doctor.hint.empty"))
 default:
  out.WriteString(tr(lang,"online.doctor.hint.data"))
 }
 return out.String()
}

func (s *BotService) onlineDoctorMarkup() tgbotapi.InlineKeyboardMarkup {
 rows:=[][]tgbotapi.InlineKeyboardButton{
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.doctor.repair"),"online_repair_ask"),
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.refresh"),"menu_online_doctor"),
  ),
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.doctor.back"),"menu_online"),
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.home"),"menu_main"),
  ),
 }
 if s.cfg.AllowMutations {rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"service.collector"),"service_ask:collector")))}
 return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func (s *BotService) sendOnlineDiagnostics(chatID int64,msgID int) {
 s.sendOrEdit(chatID,msgID,s.onlineDiagnosticText(),s.onlineDoctorMarkup())
}

func (s *BotService) askOnlineRepair(chatID int64,msgID int) {
 if !s.cfg.AllowMutations {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"online.doctor.mutations"),s.onlineDoctorMarkup())
  return
 }
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.confirm"),"online_repair_confirm"),
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"),"online_repair_cancel"),
  ),
 )
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"online.doctor.repairconfirm"),markup)
}

func (s *BotService) doOnlineRepair(chatID int64,msgID int) {
 if !s.cfg.AllowMutations {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"online.doctor.mutations"),s.onlineDoctorMarkup())
  return
 }
 if err:=ensureOnlineStatsPolicy(s.cfg);err!=nil {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"online.doctor.repairfailed",mdEsc(err.Error())),s.onlineDoctorMarkup())
  return
 }
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"online.doctor.repairdone"),s.onlineDoctorMarkup())
}
