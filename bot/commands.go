package main

import (
 "log"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Register Telegram slash-command suggestions only for an authorized private
// chat. This does not change access control; every command is still checked.
func (s *BotService) registerAdminCommands(chatID int64){
 commands:=[]tgbotapi.BotCommand{
  {Command:"start",Description:"打开控制台"},
  {Command:"users",Description:"用户管理"},
  {Command:"nodes",Description:"节点管理"},
  {Command:"traffic",Description:"用户流量统计"},
  {Command:"inboundtraffic",Description:"各节点流量"},
  {Command:"status",Description:"服务器状态"},
  {Command:"version",Description:"本地与上游版本"},
  {Command:"update",Description:"软件更新与回滚"},
  {Command:"restart",Description:"重启 Xray（需确认）"},
  {Command:"help",Description:"命令帮助"},
  {Command:"cancel",Description:"取消输入流程"},
 }
 request:=tgbotapi.NewSetMyCommandsWithScope(tgbotapi.NewBotCommandScopeChat(chatID),commands...)
 if _,err:=s.bot.Request(request);err!=nil{log.Printf("[Bot] admin command menu registration failed: %v",err)}
}
