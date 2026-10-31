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
 if item.Client.ID==""{return "",fmt.Errorf("Inbound %s has no user UUID",item.Node.Tag)}
 uri:=item.Node.GenerateVlessURI(item.Client,serverHost)
 parsed,err:=url.Parse(uri)
 if err!=nil||parsed.Scheme!="vless"||parsed.User==nil {
  return "",fmt.Errorf("Inbound %s generated an invalid link",item.Node.Tag)
 }
 if item.Node.Security=="reality" {
  q:=parsed.Query()
  if q.Get("pbk")==""{return "",fmt.Errorf("Inbound %s cannot generate the REALITY pbk public key; check the server private key",item.Node.Tag)}
  if item.Node.MLDSA65Seed!=""&&q.Get("pqv")==""{
   return "",fmt.Errorf("Inbound %s cannot generate the ML-DSA pqv parameter; check the Xray-core version and logs",item.Node.Tag)
  }
 }
 return uri,nil
}

func (s *BotService) sendUserExportMenu(chatID int64,msgID int,email string) {
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendOrEdit(chatID,msgID,"Unable to load inbounds: "+err.Error(),backToMainMarkup());return}
 entries:=boundUserExportEntries(nodes,email)
 key:=userCallbackKey(email)
 rows:=make([][]tgbotapi.InlineKeyboardButton,0,len(entries)+3)
 for _,entry:=range entries{
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("🔗 "+truncateLabel(entry.Node.Tag,29)+" :"+fmt.Sprint(entry.Node.SharePort()),
    "user_export_one:"+key+":"+userCallbackKey(entry.Node.Tag)),
  ))
 }
 if len(entries)>0{
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("📄 Export all links (TXT)","user_export_all:"+key),
  ))
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("▦ Export all QR codes","user_export_qrs:"+key),
  ))
 }
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData("👤 Back to user","manage_user:"+key),
  tgbotapi.NewInlineKeyboardButtonData("👥 User list","menu_users"),
 ))
 content:=fmt.Sprintf("📤 User %s · Export inbound links\n\n%d assigned inbounds. Choose one inbound or export all links.\n\nSingle inbound: complete link and optional QR code\nBatch TXT: one VLESS link per line for v2rayN/v2rayNG import\nBatch QR: send TXT first, then attempt individual QR images.",email,len(entries))
 if len(entries)==0{content="User "+email+" has no assigned VLESS inbounds. Assign one from user details first."}
 s.sendOrEdit(chatID,msgID,content,tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) handleUserExportCallback(cb *tgbotapi.CallbackQuery,mode string){
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID
 parts:=strings.Split(cb.Data,":")
 minParts:=2
 if mode=="one"{minParts=3}
 if len(parts)!=minParts{s.sendAlert(chatID,"Invalid export operation. Reopen the user list.");return}
 email:=lookupManagedUserByKey(s.cfg.XrayConfigPath,parts[1])
 if email==""{s.sendAlert(chatID,"User not found. Refresh the user list.");return}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,"Unable to read inbounds: "+err.Error());return}
 entries:=boundUserExportEntries(nodes,email)
 if len(entries)==0{s.sendAlert(chatID,"This user has no assigned inbounds.");return}
 if mode=="one"{
  for _,item:=range entries {
   if userCallbackKey(item.Node.Tag)==parts[2]{
    s.exportOneUserNode(chatID,email,item,GetPublicIP())
    return
   }
  }
  s.sendAlert(chatID,"The selected inbound was unassigned or deleted. Refresh the export menu.")
  return
 }
 s.exportAllUserNodes(chatID,email,entries,mode=="qrs")
}

func (s *BotService) sendUserLink(chatID int64,item userExportEntry,email,uri string) bool{
 label:=fmt.Sprintf("🔗 Inbound %s (client port %d)\nUser: %s\nTransport: %s + %s\n\n",item.Node.Tag,item.Node.SharePort(),email,item.Node.Network,item.Node.Security)
 if item.Node.SharePort()!=item.Node.Port{label+=fmt.Sprintf("Cloudflare origin listener port: %d\n",item.Node.Port)}
 payload:=label+uri
 if len(payload)>3500 {
  doc:=tgbotapi.NewDocument(chatID,tgbotapi.FileBytes{Name:fmt.Sprintf("freedom-node-%s-%d.txt",userCallbackKey(item.Node.Tag)[:8],item.Node.SharePort()),Bytes:[]byte(uri+"\n")})
  doc.Caption=fmt.Sprintf("Complete VLESS link for inbound %s (client port %d)",item.Node.Tag,item.Node.SharePort())
  if _,err:=s.bot.Send(doc);err!=nil{
   log.Printf("[Bot] VLESS TXT send failed for node %s: %v",item.Node.Tag,err)
   s.sendAlert(chatID,fmt.Sprintf("❌ Failed to send TXT for inbound %s (%d): %v",item.Node.Tag,item.Node.Port,err))
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
  s.sendAlert(chatID,fmt.Sprintf("❌ Failed to send link for inbound %s (%d): %v",item.Node.Tag,item.Node.Port,err))
  return false
 }
 return true
}

func (s *BotService) sendUserQR(chatID int64,item userExportEntry,email,uri string) bool{
 png,err:=GenerateQRCodePNG(uri,512)
 if err!=nil {
  log.Printf("[Bot] QR generation failed for %s URI bytes=%d: %v",item.Node.Tag,len(uri),err)
  s.sendAlert(chatID,fmt.Sprintf("⚠️ Inbound %s (%d): QR generation failed (URI %d bytes): %v. Use the previously sent link or TXT.",item.Node.Tag,item.Node.Port,len(uri),err))
  return false
 }
 photo:=tgbotapi.NewPhoto(chatID,tgbotapi.FileBytes{Name:fmt.Sprintf("freedom-qr-%s-%d.png",userCallbackKey(item.Node.Tag)[:8],item.Node.SharePort()),Bytes:png})
 photo.Caption=fmt.Sprintf("Inbound %s (client port %d) · %s\nQR covers only this inbound; the full URI was sent separately.",item.Node.Tag,item.Node.SharePort(),email)
 if _,err=s.bot.Send(photo);err!=nil{
  log.Printf("[Bot] QR send failed for node %s: %v",item.Node.Tag,err)
  s.sendAlert(chatID,fmt.Sprintf("⚠️ Inbound %s (%d): failed to send QR: %v. The full URI remains usable.",item.Node.Tag,item.Node.Port,err))
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
  s.sendAlert(chatID,"❌ No exportable links: "+strings.Join(issues,"; "))
  return
 }
 doc:=tgbotapi.NewDocument(chatID,tgbotapi.FileBytes{
  Name:"freedom-vless-"+userCallbackKey(email)+".txt",
  Bytes:[]byte(data),
 })
 doc.Caption=fmt.Sprintf("User %s: exported %d/%d inbounds. Each line is a complete vless:// link for v2rayN/v2rayNG.",email,len(items)-len(issues),len(items))
 if _,err:=s.bot.Send(doc);err!=nil{
  log.Printf("[Bot] batch VLESS TXT failed for user %s: %v",email,err)
  s.sendAlert(chatID,fmt.Sprintf("❌ Failed to send batch export file: %v",err))
  return
 }
 if len(issues)>0{s.sendAlert(chatID,"⚠️ Some inbounds could not generate valid sharing links:\n"+strings.Join(issues,"\n"))}
 if !includeQR{return}
 for _,item:=range items {
  uri,err:=validatedUserExportURI(item,host)
  if err!=nil{continue}
  s.sendUserQR(chatID,item,email,uri)
 }
}
