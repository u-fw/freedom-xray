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
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.disabled"),s.backToMainMarkup())
  return
 }
 if !safeUpdateMutex.TryLock() {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.busy"),s.backToMainMarkup())
  return
 }
 defer safeUpdateMutex.Unlock()
 ctx,cancel:=context.WithTimeout(context.Background(),4*time.Minute)
 defer cancel()
 results:=make([]string,0,len(components))
 for _,comp:=range components {
  if !s.cfg.AllowUpdates {results=append(results,tr(s.cfg.Language,"update.cancelled.disabled"));break}
  switch comp {
  case "script":
   commit,err:=remoteComponentVersion(ctx,"script")
   if err!=nil {results=append(results,tr(s.cfg.Language,"update.script.fetch",err.Error()));continue}
   message,err:=checkedScriptUpdate(ctx,commit)
   if err!=nil {results=append(results,tr(s.cfg.Language,"update.script.failed",err.Error()))} else {results=append(results,tr(s.cfg.Language,"update.script.ok",message))}
  case "bot":
   // checkedBotUpdate validates the release checksum and schedules an
   // independent watchdog. Its return does NOT mean the reboot passed.
   message,err:=checkedBotUpdate(ctx)
   if err!=nil {results=append(results,tr(s.cfg.Language,"update.bot.failed",err.Error()))} else {results=append(results,tr(s.cfg.Language,"update.bot.ok",message))}
  default:
   results=append(results,tr(s.cfg.Language,"update.refuse",comp))
  }
 }
 rows:=tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"update.opencenter"),"menu_update_center"),
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.main"),"menu_main")))
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.result",strings.Join(results,"\n\n")),rows)
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
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.disabled.title"),s.backToMainMarkup())
  return
 }
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
 defer cancel()
 var out strings.Builder
 out.WriteString(tr(s.cfg.Language,"update.center.title"))
 rows:=[][]tgbotapi.InlineKeyboardButton{}
 for _,comp:=range []string{"script","bot","core"} {
  if comp=="core"&&!s.cfg.AllowCoreUpdates {
   out.WriteString(tr(s.cfg.Language,"update.core.disabled"));continue
  }
  remote,err:=remoteComponentVersion(ctx,comp)
  label:=componentLabel(comp)
  current:=currentComponentVersion(comp,s.cfg.XrayBinPath)
  if err!=nil{out.WriteString(tr(s.cfg.Language,"update.fetchfailed",label,err.Error()));continue}
  if comp=="script"&&len(remote)>12{out.WriteString(tr(s.cfg.Language,"update.row.commit",label,current,remote[:12]))}else{out.WriteString(tr(s.cfg.Language,"update.row.upstream",label,current,remote))}
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"update.update.now",label),"update_ask:"+comp)))
 }
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"update.update.both"),"update_all")))
 if s.cfg.AllowCoreUpdates {
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"update.rollback"),"update_ask:core-rollback")))
 }
 out.WriteString(tr(s.cfg.Language,"update.note"))
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.main"),"menu_main")))
 s.sendOrEdit(chatID,msgID,out.String(),tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func componentLabel(comp string) string {
 switch comp {case "script":return "freedom.sh";case "bot":return "Telegram Bot";case "core":return "Xray-core";case "core-rollback":return "Xray-core rollback"}
 return comp
}

func (s *BotService) askUpdate(cb *tgbotapi.CallbackQuery,comp string) {
 if cb.Message==nil||cb.Message.Chat==nil||cb.From==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 if !s.cfg.AllowUpdates || ((comp=="core"||comp=="core-rollback")&&!s.cfg.AllowCoreUpdates) {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.unauthorized"),s.backToMainMarkup());return
 }
 if comp=="script"||comp=="bot" {
  s.runSafeUpdates(chatID,msgID,[]string{comp})
  return
 }
 if comp!="core"&&comp!="core-rollback" {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.unsupported"),s.backToMainMarkup());return
 }
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
 version:="previous"
 var err error
 if comp!="core-rollback"{version,err=remoteComponentVersion(ctx,comp)}
 if err!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.fetcherror",err.Error()),s.backToMainMarkup());return}
 var nonce [8]byte
 if _,err=rand.Read(nonce[:]);err!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.tokenfailed"),s.backToMainMarkup());return}
 token:=hex.EncodeToString(nonce[:])
 pendingUpdates.Lock()
 for id,p:=range pendingUpdates.items{if time.Now().After(p.Deadline){delete(pendingUpdates.items,id)}}
 pendingUpdates.items[token]=pendingUpdate{Component:comp,Version:version,ChatID:chatID,AdminID:cb.From.ID,Deadline:time.Now().Add(3*time.Minute)}
 pendingUpdates.Unlock()
 note:=""
 if comp=="core"||comp=="core-rollback"{note=tr(s.cfg.Language,"update.corenote")}
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.confirmbody",componentLabel(comp),version,currentComponentVersion(comp,s.cfg.XrayBinPath),note),
  tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"update.confirm"),"update_confirm:"+token),
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"),"update_cancel:"+token),
  )))
}

func (s *BotService) resolveUpdate(cb *tgbotapi.CallbackQuery,confirm bool) {
 if cb.Message==nil||cb.Message.Chat==nil||cb.From==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 token:=strings.TrimPrefix(strings.TrimPrefix(cb.Data,"update_confirm:"),"update_cancel:")
 pendingUpdates.Lock()
 pending,ok:=pendingUpdates.items[token]
 if ok&&pending.ChatID==chatID&&pending.AdminID==cb.From.ID {delete(pendingUpdates.items,token)}
 pendingUpdates.Unlock()
 if ok&&(pending.AdminID!=cb.From.ID||pending.ChatID!=chatID){s.sendAlert(chatID,tr(s.cfg.Language,"manage.owned"));return}
 if !ok || time.Now().After(pending.Deadline) {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.expired"),s.backToMainMarkup());return
 }
 if !confirm{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.cancelled"),s.backToMainMarkup());return}
 if !s.cfg.AllowUpdates || ((pending.Component=="core"||pending.Component=="core-rollback")&&!s.cfg.AllowCoreUpdates) {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.permissions"),s.backToMainMarkup());return
 }
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.verifying"),tgbotapi.InlineKeyboardMarkup{})
 ctx,cancel:=context.WithTimeout(context.Background(),3*time.Minute)
 defer cancel()
 // Check a mutable upstream release label again before downloading.
 if pending.Component=="core"||pending.Component=="bot" {
  latest,err:=remoteComponentVersion(ctx,pending.Component)
  if err!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.verifyfailed",err.Error()),s.backToMainMarkup());return}
  if latest!=pending.Version{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.changed"),s.backToMainMarkup());return}
 }
 var result string
 var err error
 switch pending.Component {
 case "script":result,err=checkedScriptUpdate(ctx,pending.Version)
 case "bot":result,err=checkedBotUpdate(ctx)
 case "core":result,err=checkedCoreUpdate(ctx,s.cfg.XrayBinPath,s.cfg.XrayConfigPath,s.cfg.StatsAPIServer,pending.Version)
 case "core-rollback":result,err=checkedCoreRollback(s.cfg.XrayBinPath,s.cfg.XrayConfigPath,s.cfg.StatsAPIServer)
 default:err=fmt.Errorf("unknown update type")
 }
 if err!=nil {
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.failed",err.Error()),s.backToMainMarkup());return
 }
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"update.resultprefix",result),s.backToMainMarkup())
}
