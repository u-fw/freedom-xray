package main

import (
 "sort"
 "strings"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func onlineCardText(state trafficStore, now time.Time, lang string) string {
 return onlineCardTextWithError(state,now,lang,nil)
}

func onlineCardTextWithError(state trafficStore,now time.Time,lang string,queryErr error) string {
 var out strings.Builder
 out.WriteString(tr(lang,"online.title"))
 out.WriteString(tr(lang,"online.history",int(onlineHistoryRetention/(24*time.Hour)),maxOnlineIPsPerUser))
 if queryErr!=nil {out.WriteString(tr(lang,"online.queryFailure",mdEsc(queryErr.Error())))}
 fresh:=state.OnlinePollAt>0 && now.Unix()-state.OnlinePollAt<=20 && state.OnlinePollAt<=now.Unix()+5
 if !fresh{out.WriteString(tr(lang,"online.stale"))}
 nameSet:=map[string]bool{}
 for name,ips:=range state.LiveIPs{if len(ips)>0{nameSet[name]=true}}
 for name,ips:=range state.OnlineIPs{if len(ips)>0{nameSet[name]=true}}
 for name:=range state.Users{nameSet[name]=true}
 names:=make([]string,0,len(nameSet))
 for name:=range nameSet{names=append(names,name)}
 sort.Slice(names,func(i,j int)bool{
  iLive:=fresh && len(state.LiveIPs[names[i]])>0
  jLive:=fresh && len(state.LiveIPs[names[j]])>0
  if iLive!=jLive{return iLive}
  iAt:=state.LastOnline[names[i]]
  jAt:=state.LastOnline[names[j]]
  if iAt!=jAt{return iAt>jAt}
  return names[i]<names[j]
 })
 if len(names)==0{
  out.WriteString(tr(lang,"online.noObservation"))
 }else{
  max:=len(names);if max>25{max=25}
  for _,name:=range names[:max]{
   current:=state.LiveIPs[name]
   live:=fresh&&len(current)>0
   status:=tr(lang,"online.recent")
   ips:=state.OnlineIPs[name]
   if len(ips)==0 {status=tr(lang,"online.notObserved")}
   if live{status=tr(lang,"online.live");ips=current}
   out.WriteString("👤 *"+mdEsc(name)+"* — "+status+"\n")
   if stamp:=state.LastOnline[name];stamp>0{
    out.WriteString(tr(lang,"online.lastSeen",time.Unix(stamp,0).Format("2006-01-02 15:04:05")))
   }else{
    out.WriteString(tr(lang,"online.lastNever"))
   }
   limit:=len(ips);if limit>3{limit=3}
   for _,ip:=range ips[:limit]{
    out.WriteString(tr(lang,"online.ip",mdCode(ip.IP),time.Unix(ip.LastSeen,0).Format("2006-01-02 15:04:05")))
   }
   usage:=state.Users[name]
   out.WriteString(tr(lang,"online.traffic",FormatBytes(usage.TotalUp),FormatBytes(usage.TotalDown),FormatBytes(usage.TotalUp+usage.TotalDown)))
   out.WriteString("\n")
  }
  if len(names)>max{out.WriteString(tr(lang,"online.truncated"))}
 }
 if fresh{out.WriteString(tr(lang,"online.polled",time.Unix(state.OnlinePollAt,0).Format("2006-01-02 15:04:05")))}
 out.WriteString(tr(lang,"online.totalScope"))
 out.WriteString(tr(lang,"online.warning"))
 return out.String()
}

func (s *BotService) sendOnlineCard(chatID int64,msgID int) {
 state,err:=refreshOnlineTraffic(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer)
 // A failed live query can still show last stored observations. Errors
 // before loading the ledger must not masquerade as an empty online list.
 if state.Users==nil{
  detail:="unknown error";if err!=nil{detail=mdEsc(err.Error())}
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"online.error",detail),s.backToMainMarkup())
  return
 }
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.refresh"),"menu_online"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.doctor"),"menu_online_doctor")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.details"),"menu_users"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.home"),"menu_main")),
 )
 s.sendOrEdit(chatID,msgID,onlineCardTextWithError(state,time.Now(),s.cfg.Language,err),markup)
}
