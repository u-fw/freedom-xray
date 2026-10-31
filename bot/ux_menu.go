package main

import (
 "context"
 "fmt"
 "strings"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Version diagnostics are read-only and remain available even when updates are disabled.
func (s *BotService) sendVersionCard(chatID int64,msgID int){
 ctx,cancel:=context.WithTimeout(context.Background(),12*time.Second);defer cancel()
 var body strings.Builder
 body.WriteString("🧩 *Version and Update Diagnostics*\n\n")
 for _,component:=range []string{"script","bot","core"} {
  local:=currentComponentVersion(component,s.cfg.XrayBinPath)
  remote,err:=remoteComponentVersion(ctx,component)
  if err!=nil{remote="Unable to fetch: "+err.Error()}
  if len(remote)==40{remote=remote[:12]}
  body.WriteString(fmt.Sprintf("*%s*\nInstalled: `%s`\nUpstream: `%s`\n\n",componentLabel(component),mdCode(local),mdCode(remote)))
 }
 if s.cfg.AllowUpdates {
  body.WriteString("Remote updates: allowed\n")
 }else{body.WriteString("Remote updates: disabled (enable on the VPS)\n")}
 if s.cfg.AllowCoreUpdates&&s.cfg.AllowUpdates{body.WriteString("Xray core updates: allowed\n")}else{body.WriteString("Xray core updates: disabled\n")}
 body.WriteString("\nNote: GitHub source updates do not automatically deploy to a VPS. Use the local freedom menu for an initial Bot upgrade.")
 keyboard:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬆️ Update center","menu_update")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔄 Refresh versions","menu_version"),tgbotapi.NewInlineKeyboardButtonData("🏠 Main menu","menu_main")),
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
