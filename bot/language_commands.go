package main

import (
 "encoding/json"
 "log"
 "os"
 "strings"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Only patch the language fields: administrator-managed bot.json files may
// contain additional settings that must survive a locale change.
func persistLanguage(path,language string) error {
 return persistLanguageWithExplicit(path,language,true)
}

// Auto-detection keeps the setting provisional (explicit=false) so a later
// Telegram locale change can still be honoured until the admin chooses one.
func persistLanguageWithExplicit(path,language string,explicit bool) error {
 raw,err:=os.ReadFile(path)
 if err!=nil{return err}
 var doc map[string]any
 if err=json.Unmarshal(raw,&doc);err!=nil{return err}
 if doc==nil{doc=map[string]any{}}
 doc["language"]=normalizeLanguage(language)
 doc["language_explicit"]=explicit
 updated,err:=json.MarshalIndent(doc,"","  ")
 if err!=nil{return err}
 return writeAtomically(path,append(updated,'\n'),0600)
}

// Map Telegram's language_code to one of the bundled locales. Unsupported
// languages deliberately return "" so the caller keeps the configured default.
func telegramLanguage(code string) string {
 switch strings.ToLower(strings.TrimSpace(code)) {
 case "en","en-us","en-gb","en-au","en-ca":
  return "en"
 case "zh","zh-cn","zh-hans","zh-sg","zh-tw","zh-hant","zh-hk":
  return "zh-CN"
 default:
  return ""
 }
}

// Language names stay in their own language; only the active choice is marked.
func languageButtonLabel(lang,current string) string {
 label:="English"
 if lang=="zh-CN" {label="\u4e2d\u6587"}
 if normalizeLanguage(current)==lang {return "● "+label}
 return label
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
 previousExplicit:=s.cfg.LanguageExplicit
 s.cfg.Language=normalizeLanguage(lang)
 s.cfg.LanguageExplicit=true
 if s.cfg.ConfigFile=="" {
  s.cfg.Language=previous
  s.cfg.LanguageExplicit=previousExplicit
  s.sendAlert(msg.Chat.ID,tr(previous,"lang.error","bot configuration file path is unavailable"))
  return
 }
 if err:=persistLanguage(s.cfg.ConfigFile,s.cfg.Language);err!=nil {
  s.cfg.Language=previous
  s.cfg.LanguageExplicit=previousExplicit
  s.sendAlert(msg.Chat.ID,tr(previous,"lang.error",err.Error()))
  return
 }
 s.registerAdminCommands(msg.Chat.ID)
 s.sendAlert(msg.Chat.ID,tr(s.cfg.Language,"lang.updated",s.cfg.Language))
 // Re-render the dashboard so /language behaves like the language button and
 // the new locale is visible immediately rather than on the next navigation.
 s.sendMainMenu(msg.Chat.ID,0)
}

// setLanguage is the dashboard-button path. It renders the menu again in the
// newly selected language and keeps the same message instead of sending a
// second dashboard.
func (s *BotService) setLanguage(chatID int64,msgID int,lang string) {
 lang=normalizeLanguage(lang)
 previous:=s.cfg.Language
 previousExplicit:=s.cfg.LanguageExplicit
 s.cfg.Language=lang
 s.cfg.LanguageExplicit=true
 if s.cfg.ConfigFile=="" {
  s.cfg.Language=previous
  s.cfg.LanguageExplicit=previousExplicit
  s.sendAlert(chatID,tr(previous,"lang.error","bot configuration file path is unavailable"))
  return
 }
 if err:=persistLanguage(s.cfg.ConfigFile,lang);err!=nil {
  s.cfg.Language=previous
  s.cfg.LanguageExplicit=previousExplicit
  s.sendAlert(chatID,tr(previous,"lang.error",err.Error()))
  return
 }
 s.registerAdminCommands(chatID)
 s.sendMainMenu(chatID,msgID)
}

// maybeAutoDetectLanguage applies Telegram's client locale until the admin
// makes an explicit choice. Only private administrator messages reach here;
// group chats and explicit selections are never overridden.
func (s *BotService) maybeAutoDetectLanguage(msg *tgbotapi.Message) {
 if s.cfg.LanguageExplicit || msg==nil || msg.From==nil || !msg.Chat.IsPrivate() {
  return
 }
 detected:=telegramLanguage(msg.From.LanguageCode)
 if detected=="" || detected==s.cfg.Language {
  return
 }
 previous:=s.cfg.Language
 s.cfg.Language=detected
 if s.cfg.ConfigFile!="" {
  if err:=persistLanguageWithExplicit(s.cfg.ConfigFile,detected,false);err!=nil {
   s.cfg.Language=previous
   log.Printf("[Bot] language auto-detect persistence failed: %v",err)
   return
  }
 }
 s.registerAdminCommands(msg.Chat.ID)
}
