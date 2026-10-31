package main

import (
 "encoding/json"
 "os"
 "strings"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Only patch the language field: administrator-managed bot.json files may
// contain additional settings that must survive a locale change.
func persistLanguage(path,language string) error {
 raw,err:=os.ReadFile(path)
 if err!=nil{return err}
 var doc map[string]any
 if err=json.Unmarshal(raw,&doc);err!=nil{return err}
 if doc==nil{doc=map[string]any{}}
 doc["language"]=normalizeLanguage(language)
 updated,err:=json.MarshalIndent(doc,"","  ")
 if err!=nil{return err}
 return writeAtomically(path,append(updated,'\n'),0600)
}

// Language selection is an administrator-private operation. Unsupported locale
// identifiers are rejected rather than silently changing the saved setting.
func (s *BotService) changeLanguage(msg *tgbotapi.Message) {
 lang:=strings.TrimSpace(msg.CommandArguments())
 if lang==""{
  s.sendAlert(msg.Chat.ID,tr(s.cfg.Language,"lang.usage",s.cfg.Language))
  return
 }
 if !supportedLanguage(lang){
  s.sendAlert(msg.Chat.ID,tr(s.cfg.Language,"lang.unsupported"))
  return
 }
 previous:=s.cfg.Language
 s.cfg.Language=normalizeLanguage(lang)
 if s.cfg.ConfigFile=="" {
  s.cfg.Language=previous
  s.sendAlert(msg.Chat.ID,tr(previous,"lang.error","bot configuration file path is unavailable"))
  return
 }
 if err:=persistLanguage(s.cfg.ConfigFile,s.cfg.Language);err!=nil {
  s.cfg.Language=previous
  s.sendAlert(msg.Chat.ID,tr(previous,"lang.error",err.Error()))
  return
 }
 s.registerAdminCommands(msg.Chat.ID)
 s.sendAlert(msg.Chat.ID,tr(s.cfg.Language,"lang.updated",s.cfg.Language))
}
