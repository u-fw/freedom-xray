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
  s.sendOrEdit(chatID,msgID,"🔒 远程更新默认关闭。请先在 VPS 的 freedom → Telegram Bot 配置中启用 allow_updates。\n\nXray-core 升级还需要单独允许 allow_core_updates。",backToMainMarkup())
  return
 }
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
 defer cancel()
 var out strings.Builder
 out.WriteString("软件更新中心（稳定版）\n\n")
 rows:=[][]tgbotapi.InlineKeyboardButton{}
 for _,comp:=range []string{"script","bot","core"} {
  if comp=="core"&&!s.cfg.AllowCoreUpdates {
   out.WriteString("Xray-core：升级未授权\n");continue
  }
  remote,err:=remoteComponentVersion(ctx,comp)
  label:=componentLabel(comp)
  current:=currentComponentVersion(comp,s.cfg.XrayBinPath)
  if err!=nil{out.WriteString(label+"：获取远端版本失败："+err.Error()+"\n");continue}
  if comp=="script"&&len(remote)>12{out.WriteString(label+"："+current+" / 提交 "+remote[:12]+"\n")}else{out.WriteString(label+"："+current+" / 远端 "+remote+"\n")}
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("检查并更新 "+label,"update_ask:"+comp)))
 }
 if s.cfg.AllowCoreUpdates {
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("↩ 回滚上一版 Xray-core","update_ask:core-rollback")))
 }
 out.WriteString("\n需要二次确认。核心更新会检查配置、保留旧内核并在启动失败时回滚。新内核仍可能影响客户端兼容性。")
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("返回主菜单","menu_main")))
 s.sendOrEdit(chatID,msgID,out.String(),tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func componentLabel(comp string) string {
 switch comp {case "script":return "freedom.sh";case "bot":return "Telegram Bot";case "core":return "Xray-core";case "core-rollback":return "Xray-core 回滚"}
 return comp
}

func (s *BotService) askUpdate(cb *tgbotapi.CallbackQuery,comp string) {
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 if !s.cfg.AllowUpdates || ((comp=="core"||comp=="core-rollback")&&!s.cfg.AllowCoreUpdates) {
  s.sendOrEdit(chatID,msgID,"此更新操作尚未在服务器端授权。",backToMainMarkup());return
 }
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
 version:="previous"
 var err error
 if comp!="core-rollback"{version,err=remoteComponentVersion(ctx,comp)}
 if err!=nil{s.sendOrEdit(chatID,msgID,"获取版本失败："+err.Error(),backToMainMarkup());return}
 var nonce [8]byte
 if _,err=rand.Read(nonce[:]);err!=nil{s.sendOrEdit(chatID,msgID,"随机确认码生成失败",backToMainMarkup());return}
 token:=hex.EncodeToString(nonce[:])
 pendingUpdates.Lock()
 for id,p:=range pendingUpdates.items{if time.Now().After(p.Deadline){delete(pendingUpdates.items,id)}}
 pendingUpdates.items[token]=pendingUpdate{Component:comp,Version:version,ChatID:chatID,AdminID:cb.From.ID,Deadline:time.Now().Add(3*time.Minute)}
 pendingUpdates.Unlock()
 note:=""
 if comp=="core"||comp=="core-rollback"{note="\n注意：核心升级可能导致 REALITY / ML-KEM 与部分客户端不兼容，请保留至少一个备用管理通道。"}
 s.sendOrEdit(chatID,msgID,fmt.Sprintf("确认更新 %s？\n目标：%s\n当前：%s%s\n\n确认码有效期 3 分钟。",componentLabel(comp),version,currentComponentVersion(comp,s.cfg.XrayBinPath),note),
  tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("确认更新","update_confirm:"+token),
   tgbotapi.NewInlineKeyboardButtonData("取消","update_cancel:"+token),
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
  s.sendOrEdit(chatID,msgID,"更新授权已失效，请重新检查版本。",backToMainMarkup());return
 }
 if !confirm{s.sendOrEdit(chatID,msgID,"已取消更新。",backToMainMarkup());return}
 if !s.cfg.AllowUpdates || ((pending.Component=="core"||pending.Component=="core-rollback")&&!s.cfg.AllowCoreUpdates) {
  s.sendOrEdit(chatID,msgID,"更新权限已关闭。",backToMainMarkup());return
 }
 s.sendOrEdit(chatID,msgID,"正在校验发布包并执行更新。",tgbotapi.InlineKeyboardMarkup{})
 ctx,cancel:=context.WithTimeout(context.Background(),3*time.Minute)
 defer cancel()
 // Check a mutable upstream release label again before downloading.
 if pending.Component=="core"||pending.Component=="bot" {
  latest,err:=remoteComponentVersion(ctx,pending.Component)
  if err!=nil{s.sendOrEdit(chatID,msgID,"版本确认失败："+err.Error(),backToMainMarkup());return}
  if latest!=pending.Version{s.sendOrEdit(chatID,msgID,"远端版本发生变化，请重新确认。",backToMainMarkup());return}
 }
 var result string
 var err error
 switch pending.Component {
 case "script":result,err=checkedScriptUpdate(ctx,pending.Version)
 case "bot":result,err=checkedBotUpdate(ctx)
 case "core":result,err=checkedCoreUpdate(ctx,s.cfg.XrayBinPath,s.cfg.XrayConfigPath,pending.Version)
 case "core-rollback":result,err=checkedCoreRollback(s.cfg.XrayBinPath,s.cfg.XrayConfigPath)
 default:err=fmt.Errorf("未知更新类型")
 }
 if err!=nil {
  s.sendOrEdit(chatID,msgID,"更新失败："+err.Error(),backToMainMarkup());return
 }
 s.sendOrEdit(chatID,msgID,"更新结果："+result,backToMainMarkup())
}
