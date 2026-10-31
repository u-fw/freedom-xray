package main

import (
 "fmt"
 "log"
 "net/url"
 "sort"
 "strings"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// A user only receives links for VLESS inbounds explicitly bound to that user.
// The global user directory may contain accounts without any inbound bindings.
type userExportEntry struct {
 Node InboundDetails
 Client XrayClient
}

func boundUserExportEntries(nodes []InboundDetails, email string) []userExportEntry {
 var items []userExportEntry
 for _,node:=range nodes {
  if node.Protocol!="vless" {continue}
  for _,client:=range node.Clients{
   if client.Email==email {
    items=append(items,userExportEntry{Node:node,Client:client})
   }
  }
 }
 sort.SliceStable(items,func(i,j int)bool{
  if items[i].Node.Port!=items[j].Node.Port{return items[i].Node.Port<items[j].Node.Port}
  return items[i].Node.Tag<items[j].Node.Tag
 })
 return items
}

func validatedUserExportURI(item userExportEntry, serverHost string) (string,error) {
 if item.Client.ID==""{return "",fmt.Errorf("节点 %s 缺少用户 UUID",item.Node.Tag)}
 uri:=item.Node.GenerateVlessURI(item.Client,serverHost)
 parsed,err:=url.Parse(uri)
 if err!=nil||parsed.Scheme!="vless"||parsed.User==nil {
  return "",fmt.Errorf("节点 %s 生成的链接格式无效",item.Node.Tag)
 }
 if item.Node.Security=="reality" {
  q:=parsed.Query()
  if q.Get("pbk")==""{return "",fmt.Errorf("节点 %s 无法生成 REALITY 公钥 pbk，请检查服务器私钥",item.Node.Tag)}
  if item.Node.MLDSA65Seed!=""&&q.Get("pqv")==""{
   return "",fmt.Errorf("节点 %s 无法生成 ML-DSA 验证参数 pqv，请检查 Xray-core 版本和日志",item.Node.Tag)
  }
 }
 return uri,nil
}

func (s *BotService) sendUserExportMenu(chatID int64,msgID int,email string) {
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendOrEdit(chatID,msgID,"读取节点失败："+err.Error(),backToMainMarkup());return}
 entries:=boundUserExportEntries(nodes,email)
 key:=userCallbackKey(email)
 rows:=make([][]tgbotapi.InlineKeyboardButton,0,len(entries)+3)
 for _,entry:=range entries{
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("🔗 "+truncateLabel(entry.Node.Tag,29)+" :"+fmt.Sprint(entry.Node.Port),
    "user_export_one:"+key+":"+userCallbackKey(entry.Node.Tag)),
  ))
 }
 if len(entries)>0{
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("📄 批量导出全部链接（TXT）","user_export_all:"+key),
  ))
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("▦ 批量导出全部二维码","user_export_qrs:"+key),
  ))
 }
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData("👤 返回用户详情","manage_user:"+key),
  tgbotapi.NewInlineKeyboardButtonData("👥 用户列表","menu_users"),
 ))
 content:=fmt.Sprintf("📤 用户 %s · 导出节点\n\n已绑定 %d 个节点。请选择单节点导出，或一次导出全部链接。\n\n单节点：完整链接 + 可选二维码\n批量 TXT：每行一条 VLESS 链接，方便复制/导入 v2rayN、v2rayNG\n批量二维码：先发送 TXT，再逐个尝试生成二维码。",email,len(entries))
 if len(entries)==0{content="用户 "+email+" 目前没有绑定任何 VLESS 节点。请先到用户详情绑定节点。"}
 s.sendOrEdit(chatID,msgID,content,tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) handleUserExportCallback(cb *tgbotapi.CallbackQuery,mode string){
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID
 parts:=strings.Split(cb.Data,":")
 minParts:=2
 if mode=="one"{minParts=3}
 if len(parts)!=minParts{s.sendAlert(chatID,"无效的导出操作，请重新打开用户列表。");return}
 email:=lookupManagedUserByKey(s.cfg.XrayConfigPath,parts[1])
 if email==""{s.sendAlert(chatID,"用户已不存在，请刷新用户列表。");return}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,"节点读取失败："+err.Error());return}
 entries:=boundUserExportEntries(nodes,email)
 if len(entries)==0{s.sendAlert(chatID,"此用户还没有绑定任何节点。");return}
 if mode=="one"{
  for _,item:=range entries {
   if userCallbackKey(item.Node.Tag)==parts[2]{
    s.exportOneUserNode(chatID,email,item,GetPublicIP())
    return
   }
  }
  s.sendAlert(chatID,"所选节点已解绑或被删除，请刷新导出菜单。")
  return
 }
 s.exportAllUserNodes(chatID,email,entries,mode=="qrs")
}

func (s *BotService) sendUserLink(chatID int64,item userExportEntry,email,uri string) bool{
 label:=fmt.Sprintf("🔗 节点 %s (%d)\n用户：%s\n类型：%s + %s\n\n",item.Node.Tag,item.Node.Port,email,item.Node.Network,item.Node.Security)
 payload:=label+uri
 if len(payload)>3500 {
  doc:=tgbotapi.NewDocument(chatID,tgbotapi.FileBytes{Name:fmt.Sprintf("freedom-node-%d.txt",item.Node.Port),Bytes:[]byte(uri+"\n")})
  doc.Caption=fmt.Sprintf("节点 %s (%d) 的完整 VLESS 链接，超长参数以 TXT 发送",item.Node.Tag,item.Node.Port)
  if _,err:=s.bot.Send(doc);err!=nil{
   log.Printf("[Bot] VLESS TXT send failed for node %s: %v",item.Node.Tag,err)
   s.sendAlert(chatID,fmt.Sprintf("❌ 节点 %s (%d) 文本文件发送失败：%v",item.Node.Tag,item.Node.Port,err))
   return false
  }
  return true
 }
 msg:=tgbotapi.NewMessage(chatID,payload)
 msg.DisableWebPagePreview=true
 // Plain text is essential: post-quantum query parameters often contain
 // Markdown metacharacters. Never trim, reformat or Markdown-escape the URI.
 if _,err:=s.bot.Send(msg);err!=nil{
  log.Printf("[Bot] VLESS text send failed for node %s: %v",item.Node.Tag,err)
  s.sendAlert(chatID,fmt.Sprintf("❌ 节点 %s (%d) 链接发送失败：%v",item.Node.Tag,item.Node.Port,err))
  return false
 }
 return true
}

func (s *BotService) sendUserQR(chatID int64,item userExportEntry,email,uri string) bool{
 png,err:=GenerateQRCodePNG(uri,512)
 if err!=nil {
  log.Printf("[Bot] QR generation failed for %s URI bytes=%d: %v",item.Node.Tag,len(uri),err)
  s.sendAlert(chatID,fmt.Sprintf("⚠️ 节点 %s (%d)：二维码生成失败（链接 %d 字节）：%v。请使用已发送的文本链接或 TXT 导入。",item.Node.Tag,item.Node.Port,len(uri),err))
  return false
 }
 photo:=tgbotapi.NewPhoto(chatID,tgbotapi.FileBytes{Name:fmt.Sprintf("freedom-qr-%d.png",item.Node.Port),Bytes:png})
 photo.Caption=fmt.Sprintf("节点 %s (%d) · %s\n二维码只包含当前节点；完整链接已另行发送。",item.Node.Tag,item.Node.Port,email)
 if _,err=s.bot.Send(photo);err!=nil{
  log.Printf("[Bot] QR send failed for node %s: %v",item.Node.Tag,err)
  s.sendAlert(chatID,fmt.Sprintf("⚠️ 节点 %s (%d)：二维码发送失败：%v。完整链接仍可使用。",item.Node.Tag,item.Node.Port,err))
  return false
 }
 return true
}

func (s *BotService) exportOneUserNode(chatID int64,email string,item userExportEntry,host string){
 uri,err:=validatedUserExportURI(item,host)
 if err!=nil{s.sendAlert(chatID,"❌ "+err.Error());return}
 if !s.sendUserLink(chatID,item,email,uri){return}
 s.sendUserQR(chatID,item,email,uri)
}

func assembleBatchUserLinks(items []userExportEntry,host string)(string,[]string){
 links:=make([]string,0,len(items))
 var issues []string
 for _,item:=range items{
  uri,err:=validatedUserExportURI(item,host)
  if err!=nil {issues=append(issues,err.Error());continue}
  links=append(links,uri)
 }
 return strings.Join(links,"\n")+"\n",issues
}

func (s *BotService) exportAllUserNodes(chatID int64,email string,items []userExportEntry,includeQR bool){
 host:=GetPublicIP()
 data,issues:=assembleBatchUserLinks(items,host)
 if len(strings.TrimSpace(data))==0 {
  s.sendAlert(chatID,"❌ 没有可导出的链接："+strings.Join(issues,"；"))
  return
 }
 doc:=tgbotapi.NewDocument(chatID,tgbotapi.FileBytes{
  Name:"freedom-vless-"+userCallbackKey(email)+".txt",
  Bytes:[]byte(data),
 })
 doc.Caption=fmt.Sprintf("用户 %s：批量导出 %d/%d 个节点。每行一个完整 vless:// 链接，可用于 v2rayN/v2rayNG 导入。",email,len(items)-len(issues),len(items))
 if _,err:=s.bot.Send(doc);err!=nil{
  log.Printf("[Bot] batch VLESS TXT failed for user %s: %v",email,err)
  s.sendAlert(chatID,fmt.Sprintf("❌ 批量导出文件发送失败：%v",err))
  return
 }
 if len(issues)>0{s.sendAlert(chatID,"⚠️ 以下节点无法生成可用分享链接：\n"+strings.Join(issues,"\n"))}
 if !includeQR{return}
 for _,item:=range items {
  uri,err:=validatedUserExportURI(item,host)
  if err!=nil{continue}
  s.sendUserQR(chatID,item,email,uri)
 }
}
