package main

import (
 "strings"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func onlineCardText(state trafficStore, now time.Time, lang string) string {
 var out strings.Builder
 out.WriteString(tr(lang,"online.title"))
 if state.OnlinePollAt<=0 || now.Unix()-state.OnlinePollAt>20 {
  out.WriteString(tr(lang,"online.unavailable"))
 }else{
  count:=0
  for name,ips:=range state.LiveIPs {
   if len(ips)==0{continue}
   count++
   if count>40{break}
   out.WriteString(tr(lang,"online.client",mdEsc(name),len(ips)))
   for _,ip:=range ips{
    out.WriteString("  • "+mdCode(ip.IP)+"\n")
   }
  }
  if count==0 {out.WriteString(tr(lang,"online.empty"))}
  if count>40 {out.WriteString(tr(lang,"online.truncated"))}
  out.WriteString(tr(lang,"online.polled",time.Unix(state.OnlinePollAt,0).Format("2006-01-02 15:04:05")))
 }
 out.WriteString(tr(lang,"online.warning"))
 return out.String()
}
func (s *BotService) sendOnlineCard(chatID int64,msgID int) {
 state,err:=loadTrafficStore(s.cfg.XrayConfigPath)
 if err!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"online.error",mdEsc(err.Error())),backToMainMarkup());return}
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.refresh"),"menu_online"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"online.details"),"menu_users")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.home"),"menu_main")),
 )
 s.sendOrEdit(chatID,msgID,onlineCardText(state,time.Now(),s.cfg.Language),markup)
}
