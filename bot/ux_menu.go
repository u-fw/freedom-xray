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
 body.WriteString("🧩 *版本与更新诊断*\n\n")
 for _,component:=range []string{"script","bot","core"} {
  local:=currentComponentVersion(component,s.cfg.XrayBinPath)
  remote,err:=remoteComponentVersion(ctx,component)
  if err!=nil{remote="无法读取："+err.Error()}
  if len(remote)==40{remote=remote[:12]}
  body.WriteString(fmt.Sprintf("*%s*\n当前：`%s`\n上游：`%s`\n\n",componentLabel(component),mdCode(local),mdCode(remote)))
 }
 if s.cfg.AllowUpdates {
  body.WriteString("远程更新：已授权\n")
 }else{body.WriteString("远程更新：未授权（需要 VPS 本地开启）\n")}
 if s.cfg.AllowCoreUpdates&&s.cfg.AllowUpdates{body.WriteString("Xray 核心更新：已授权\n")}else{body.WriteString("Xray 核心更新：未授权\n")}
 body.WriteString("\n注意：GitHub 源码更新不等于 VPS 已部署；首次升级旧 Bot 需要本机 freedom 菜单。")
 keyboard:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬆️ 打开更新中心","menu_update")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔄 刷新版本","menu_version"),tgbotapi.NewInlineKeyboardButtonData("🏠 主菜单","menu_main")),
 )
 s.sendOrEdit(chatID,msgID,body.String(),keyboard)
}

func (s *BotService) sendHelpCard(chatID int64,msgID int){
 msg:="📖 *Freedom Bot 使用帮助*\n\n"+
  "① `/start` — 打开控制台\n"+
  "② `/users` — 管理用户、UUID 和节点分配\n"+
  "③ `/nodes` — 节点列表与 XHTTP 配置\n"+
  "④ `/traffic` — 用户流量，`/inboundtraffic` — 入站流量\n"+
  "⑤ `/version` — 本地和上游版本对比\n"+
  "⑥ `/update` — 更新脚本、Bot 或 Xray-core（需本机授权）\n"+
  "⑦ `/restart` — 确认后重启 Xray\n\n"+
  "常用快捷指令：`/adduser phone`、`/disableuser phone`、`/enableuser phone`。"+
  "\n\n🔒 管理操作仅允许授权用户私聊，修改和更新需二次确认。"
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("👥 用户管理","menu_users"),tgbotapi.NewInlineKeyboardButtonData("⬆️ 更新中心","menu_update")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🏠 主菜单","menu_main")),
 )
 s.sendOrEdit(chatID,msgID,msg,markup)
}
