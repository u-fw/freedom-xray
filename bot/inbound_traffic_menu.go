package main

import (
 "context"
 "fmt"
 "strings"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (s *BotService) sendInboundTrafficCard(chatID int64,msgID int) {
 ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
 traffic,err:=QueryInboundTraffic(ctx,s.cfg.XrayBinPath,s.cfg.StatsAPIServer)
 if err!=nil {s.sendOrEdit(chatID,msgID,"读取入站统计失败："+mdEsc(err.Error()),backToMainMarkup());return}
 var text strings.Builder
 text.WriteString("📡 *各节点总流量（Xray 当前运行周期）*\n\n")
 var sum uint64
 if len(traffic)==0 {text.WriteString("目前没有入站流量数据。请确认 statsInboundUplink/Downlink 已启用。\n")}
 for _,t:=range traffic{
  if t.Tag=="api-internal"{continue}
  sum+=t.Total()
  text.WriteString(fmt.Sprintf("*%s*：⬆️ %s / ⬇️ %s，合计 %s\n",mdEsc(t.Tag),FormatBytes(t.Uplink),FormatBytes(t.Downlink),FormatBytes(t.Total())))
 }
 text.WriteString(fmt.Sprintf("\n统计合计：%s\n",FormatBytes(sum)))
 text.WriteString("注意：这里是入站整体流量，不是“单用户×单入站”统计；Xray 重启后计数器从零开始。")
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⚡ 刷新","menu_inbound_traffic"),tgbotapi.NewInlineKeyboardButtonData("👥 用户流量","menu_traffic")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔙 主菜单","menu_main")),
 )
 s.sendOrEdit(chatID,msgID,text.String(),markup)
}
