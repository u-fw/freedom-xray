package main

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "fmt"
 "strings"
 "sync"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var safeUpdateMutex sync.Mutex

// Explicit /update means consent to update management components only.
// Xray-core and rollback always retain a separate, time-limited confirmation.
func (s *BotService) runSafeUpdates(chatID int64,msgID int,components []string) {
 if !s.cfg.AllowUpdates {
  s.sendOrEdit(chatID,msgID,"Remote updates are disabled. Enable allow_updates on the VPS. Core updates additionally require allow_core_updates.",backToMainMarkup())
  return
 }
 if !safeUpdateMutex.TryLock() {
  s.sendOrEdit(chatID,msgID,"Another management component update is already running.",backToMainMarkup())
  return
 }
 defer safeUpdateMutex.Unlock()
 ctx,cancel:=context.WithTimeout(context.Background(),4*time.Minute)
 defer cancel()
 results:=make([]string,0,len(components))
 for _,comp:=range components {
  if !s.cfg.AllowUpdates {results=append(results,"Cancelled: updates disabled");break}
  switch comp {
  case "script":
   commit,err:=remoteComponentVersion(ctx,"script")
   if err!=nil {results=append(results,"freedom.sh: failed to fetch version: "+err.Error());continue}
   message,err:=checkedScriptUpdate(ctx,commit)
   if err!=nil {results=append(results,"freedom.sh: update failed: "+err.Error())} else {results=append(results,"freedom.sh: "+message)}
  case "bot":
   // checkedBotUpdate validates the release checksum and schedules an
   // independent watchdog. Its return does NOT mean the reboot passed.
   message,err:=checkedBotUpdate(ctx)
   if err!=nil {results=append(results,"Telegram Bot: update failed: "+err.Error())} else {results=append(results,"Telegram Bot: "+message)}
  default:
   results=append(results,"Refusing an unconfirmed update component: "+comp)
  }
 }
 rows:=tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData("Open update center (core needs confirmation)","menu_update"),
  tgbotapi.NewInlineKeyboardButtonData("Main menu","menu_main")))
 s.sendOrEdit(chatID,msgID,"Management component update results:\n\n"+strings.Join(results,"\n\n")+"\n\nXray-core was not modified. Verify Bot self-updates with the independent health check.",rows)
}

type pendingUpdate struct {
 Component string
 Version string
 AdminID int64
 ChatID int64
 Deadline time.Time
}

var pendingUpdates = struct{
 sync.Mutex
 items map[string]pendingUpdate
}{items:map[string]pendingUpdate{}}

func (s *BotService) showUpdatesMenu(chatID int64,msgID int) {
 if !s.cfg.AllowUpdates {
  s.sendOrEdit(chatID,msgID,"🔒 Remote updates are disabled by default. Enable allow_updates in the VPS Telegram Bot settings.\n\nCore upgrades also need allow_core_updates.",backToMainMarkup())
  return
 }
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
 defer cancel()
 var out strings.Builder
 out.WriteString("Software Update Center (manual)\n\n")
 rows:=[][]tgbotapi.InlineKeyboardButton{}
 for _,comp:=range []string{"script","bot","core"} {
  if comp=="core"&&!s.cfg.AllowCoreUpdates {
   out.WriteString("Xray-core: core upgrade disabled\n");continue
  }
  remote,err:=remoteComponentVersion(ctx,comp)
  label:=componentLabel(comp)
  current:=currentComponentVersion(comp,s.cfg.XrayBinPath)
  if err!=nil{out.WriteString(label+" : failed to fetch remote version: "+err.Error()+"\n");continue}
  if comp=="script"&&len(remote)>12{out.WriteString(label+"："+current+" / commit "+remote[:12]+"\n")}else{out.WriteString(label+"："+current+" / upstream "+remote+"\n")}
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Update now: "+label,"update_ask:"+comp)))
 }
 if s.cfg.AllowCoreUpdates {
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("↩ Roll back Xray-core","update_ask:core-rollback")))
 }
 out.WriteString("\nManager/Bot updates run upon selection after validation; core upgrades and rollbacks require confirmation. Bot updates are verified by an independent health check.")
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Back to main menu","menu_main")))
 s.sendOrEdit(chatID,msgID,out.String(),tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func componentLabel(comp string) string {
 switch comp {case "script":return "freedom.sh";case "bot":return "Telegram Bot";case "core":return "Xray-core";case "core-rollback":return "Xray-core rollback"}
 return comp
}

func (s *BotService) askUpdate(cb *tgbotapi.CallbackQuery,comp string) {
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 if !s.cfg.AllowUpdates || ((comp=="core"||comp=="core-rollback")&&!s.cfg.AllowCoreUpdates) {
  s.sendOrEdit(chatID,msgID,"This update is not authorized on the VPS.",backToMainMarkup());return
 }
 if comp=="script"||comp=="bot" {
  s.runSafeUpdates(chatID,msgID,[]string{comp})
  return
 }
 if comp!="core"&&comp!="core-rollback" {
  s.sendOrEdit(chatID,msgID,"Unsupported update component.",backToMainMarkup());return
 }
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
 version:="previous"
 var err error
 if comp!="core-rollback"{version,err=remoteComponentVersion(ctx,comp)}
 if err!=nil{s.sendOrEdit(chatID,msgID,"Unable to fetch version: "+err.Error(),backToMainMarkup());return}
 var nonce [8]byte
 if _,err=rand.Read(nonce[:]);err!=nil{s.sendOrEdit(chatID,msgID,"Unable to generate confirmation token",backToMainMarkup());return}
 token:=hex.EncodeToString(nonce[:])
 pendingUpdates.Lock()
 for id,p:=range pendingUpdates.items{if time.Now().After(p.Deadline){delete(pendingUpdates.items,id)}}
 pendingUpdates.items[token]=pendingUpdate{Component:comp,Version:version,ChatID:chatID,AdminID:cb.From.ID,Deadline:time.Now().Add(3*time.Minute)}
 pendingUpdates.Unlock()
 note:=""
 if comp=="core"||comp=="core-rollback"{note="\nWarning: upgrading Xray-core can break REALITY / ML-KEM compatibility with older clients. Keep a separate recovery path."}
 s.sendOrEdit(chatID,msgID,fmt.Sprintf("Confirm update of %s?\nTarget: %s\nCurrent: %s%s\n\nConfirmation expires in 3 minutes.",componentLabel(comp),version,currentComponentVersion(comp,s.cfg.XrayBinPath),note),
  tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("Confirm update","update_confirm:"+token),
   tgbotapi.NewInlineKeyboardButtonData("Cancel","update_cancel:"+token),
  )))
}

func (s *BotService) resolveUpdate(cb *tgbotapi.CallbackQuery,confirm bool) {
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 token:=strings.TrimPrefix(strings.TrimPrefix(cb.Data,"update_confirm:"),"update_cancel:")
 pendingUpdates.Lock()
 pending,ok:=pendingUpdates.items[token]
 if ok {delete(pendingUpdates.items,token)}
 pendingUpdates.Unlock()
 if !ok || time.Now().After(pending.Deadline) || pending.AdminID!=cb.From.ID || pending.ChatID!=chatID {
  s.sendOrEdit(chatID,msgID,"Update authorization expired; check the version again.",backToMainMarkup());return
 }
 if !confirm{s.sendOrEdit(chatID,msgID,"Update cancelled.",backToMainMarkup());return}
 if !s.cfg.AllowUpdates || ((pending.Component=="core"||pending.Component=="core-rollback")&&!s.cfg.AllowCoreUpdates) {
  s.sendOrEdit(chatID,msgID,"Update permissions are disabled.",backToMainMarkup());return
 }
 s.sendOrEdit(chatID,msgID,"Verifying the release package and performing the update.",tgbotapi.InlineKeyboardMarkup{})
 ctx,cancel:=context.WithTimeout(context.Background(),3*time.Minute)
 defer cancel()
 // Check a mutable upstream release label again before downloading.
 if pending.Component=="core"||pending.Component=="bot" {
  latest,err:=remoteComponentVersion(ctx,pending.Component)
  if err!=nil{s.sendOrEdit(chatID,msgID,"Version verification failed: "+err.Error(),backToMainMarkup());return}
  if latest!=pending.Version{s.sendOrEdit(chatID,msgID,"The upstream version changed; confirm again.",backToMainMarkup());return}
 }
 var result string
 var err error
 switch pending.Component {
 case "script":result,err=checkedScriptUpdate(ctx,pending.Version)
 case "bot":result,err=checkedBotUpdate(ctx)
 case "core":result,err=checkedCoreUpdate(ctx,s.cfg.XrayBinPath,s.cfg.XrayConfigPath,pending.Version)
 case "core-rollback":result,err=checkedCoreRollback(s.cfg.XrayBinPath,s.cfg.XrayConfigPath)
 default:err=fmt.Errorf("Unknown update type")
 }
 if err!=nil {
  s.sendOrEdit(chatID,msgID,"Update failed: "+err.Error(),backToMainMarkup());return
 }
 s.sendOrEdit(chatID,msgID,"Update result: "+result,backToMainMarkup())
}
