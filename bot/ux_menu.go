package main

import (
 "context"
 "strings"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Version diagnostics are read-only and remain available even when updates are disabled.
func (s *BotService) sendVersionCard(chatID int64,msgID int){
 ctx,cancel:=context.WithTimeout(context.Background(),12*time.Second);defer cancel()
 var body strings.Builder
 body.WriteString(tr(s.cfg.Language,"version.title"))
 for _,component:=range []string{"script","bot","core"} {
  local:=currentComponentVersion(component,s.cfg.XrayBinPath)
  remote,err:=remoteComponentVersion(ctx,component)
  if err!=nil{remote=tr(s.cfg.Language,"version.fetchfailed",err.Error())}
  if len(remote)==40{remote=remote[:12]}
  body.WriteString(tr(s.cfg.Language,"version.component",componentLabel(component),mdCode(local),mdCode(remote)))
 }
 if s.cfg.AllowUpdates {
  body.WriteString(tr(s.cfg.Language,"version.updates.allowed"))
 }else{body.WriteString(tr(s.cfg.Language,"version.updates.disabled"))}
 if s.cfg.AllowCoreUpdates&&s.cfg.AllowUpdates{body.WriteString(tr(s.cfg.Language,"version.core.allowed"))}else{body.WriteString(tr(s.cfg.Language,"version.core.disabled"))}
 body.WriteString(tr(s.cfg.Language,"version.note"))
 keyboard:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"version.updatecenter"),"menu_update")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"version.refresh"),"menu_version"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"version.mainmenu"),"menu_main")),
 )
 s.sendOrEdit(chatID,msgID,body.String(),keyboard)
}

func (s *BotService) sendHelpCard(chatID int64,msgID int){
 msg:=tr(s.cfg.Language,"help.body")
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.users"),"menu_users"),tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"help.update"),"menu_update")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"menu.home"),"menu_main")),
 )
 s.sendOrEdit(chatID,msgID,msg,markup)
}
