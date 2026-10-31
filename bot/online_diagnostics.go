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
 Level string
 Enabled bool
 Clients int
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
  settings,_:=getMap(in["settings"])
  list,_:=settings["clients"].([]any)
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
  enabled:=false
  if settings,ok:=root.Policy.Levels[level];ok {
   enabled=settings["statsUserOnline"]==true
  }
  result=append(result,onlineLevelInfo{Level:level,Enabled:enabled,Clients:clients[level]})
 }
 return result,nil
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
 return tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.doctor.repair"),"online_repair_ask"),
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.refresh"),"menu_online_doctor"),
  ),
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.doctor.back"),"menu_online"),
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.home"),"menu_main"),
  ),
 )
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
