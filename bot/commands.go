package main

import (
 "log"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Register Telegram slash-command suggestions only for an authorized private
// chat. This does not change access control; every command is still checked.
func (s *BotService) registerAdminCommands(chatID int64){
 commands:=[]tgbotapi.BotCommand{
  {Command:"start",Description:tr(s.cfg.Language,"command.start")},
  {Command:"users",Description:tr(s.cfg.Language,"command.users")},
  {Command:"nodes",Description:tr(s.cfg.Language,"command.nodes")},
  {Command:"traffic",Description:tr(s.cfg.Language,"command.traffic")},
  {Command:"online",Description:tr(s.cfg.Language,"command.online")},
  {Command:"inboundtraffic",Description:tr(s.cfg.Language,"command.inboundtraffic")},
  {Command:"status",Description:tr(s.cfg.Language,"command.status")},
  {Command:"version",Description:tr(s.cfg.Language,"command.version")},
  {Command:"update",Description:tr(s.cfg.Language,"command.update")},
  {Command:"restart",Description:tr(s.cfg.Language,"command.restart")},
  {Command:"help",Description:tr(s.cfg.Language,"command.help")},
  {Command:"cancel",Description:tr(s.cfg.Language,"command.cancel")},
  {Command:"language",Description:tr(s.cfg.Language,"command.language")},
 }
 request:=tgbotapi.NewSetMyCommandsWithScope(tgbotapi.NewBotCommandScopeChat(chatID),commands...)
 if _,err:=s.bot.Request(request);err!=nil{log.Printf("[Bot] admin command menu registration failed: %v",err)}
}
