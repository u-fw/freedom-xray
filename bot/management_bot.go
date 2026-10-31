package main

import (
 "crypto/rand"
 "encoding/hex"
 "fmt"
 "log"
 "strings"
 "sync"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type pendingUserChange struct {
 Change UserChange
 ChatID int64
 AdminID int64
 ExpiresAt time.Time
}

var pendingUserChanges = struct{
 sync.Mutex
 items map[string]pendingUserChange
}{items:make(map[string]pendingUserChange)}

func (s *BotService) offerUserChange(chatID,adminID int64,messageID int,change UserChange) {
 if !s.cfg.AllowMutations {
  s.sendOrEdit(chatID,messageID,"🔒 Remote configuration changes are disabled. Enable allow_mutations in the VPS Telegram Bot settings. Read-only features remain available.",backToMainMarkup())
  return
 }
 var token [8]byte
 if _,err:=rand.Read(token[:]);err!=nil {s.sendAlert(chatID,"Unable to generate a secure token: "+err.Error());return}
 key:=hex.EncodeToString(token[:])
 pendingUserChanges.Lock()
 now:=time.Now()
 for k,v:=range pendingUserChanges.items {if now.After(v.ExpiresAt){delete(pendingUserChanges.items,k)}}
 pendingUserChanges.items[key]=pendingUserChange{Change:change,ChatID:chatID,AdminID:adminID,ExpiresAt:now.Add(3*time.Minute)}
 pendingUserChanges.Unlock()

 var description string
 switch change.Kind {
 case "add":
   if change.Node=="" {description=fmt.Sprintf("Create user %s without assigning an inbound",change.User)} else {description=fmt.Sprintf("Add user %s (inbound: %s)",change.User,scopeLabel(change.Node))}
 case "rename": description=fmt.Sprintf("Rename user %s to %s",change.User,change.NewName)
 case "rotate": description=fmt.Sprintf("Rotate %s's UUID; old links will stop working immediately",change.User)
 case "delete": description=fmt.Sprintf("Delete %s from every inbound; existing links will stop working",change.User)
 case "assign": description=fmt.Sprintf("Assign %s to inbound %s",change.User,change.Node)
 case "unassign": description=fmt.Sprintf("Unassign %s from inbound %s",change.User,change.Node)
  case "disable": description=fmt.Sprintf("Suspend user %s; existing links stop working while the UUID is kept locally",change.User)
  case "enable": description=fmt.Sprintf("Restore user %s's original UUID and inbound assignments",change.User)
 default: s.sendAlert(chatID,"Unknown management operation");return
 }
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("✅ Confirm change","manage_confirm:"+key),
   tgbotapi.NewInlineKeyboardButtonData("Cancel","manage_cancel:"+key),
  ),
 )
 s.sendOrEdit(chatID,messageID,"⚠️ Confirm change:\n"+description+"\n\nConfirmation expires after 3 minutes. Changes are validated before restarting Xray; failures trigger rollback.",markup)
}

func scopeLabel(node string) string {if node==""||node=="all"{return "All inbounds"};return node}

func (s *BotService) resolveUserChange(cb *tgbotapi.CallbackQuery,confirm bool) {
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID
 msgID:=cb.Message.MessageID
 key:=strings.TrimPrefix(strings.TrimPrefix(cb.Data,"manage_confirm:"),"manage_cancel:")
 pendingUserChanges.Lock()
 request,ok:=pendingUserChanges.items[key]
 if ok {delete(pendingUserChanges.items,key)}
 pendingUserChanges.Unlock()
 if !ok || request.ChatID!=chatID || request.AdminID!=cb.From.ID || time.Now().After(request.ExpiresAt) {
  s.sendOrEdit(chatID,msgID,"The operation has expired or is invalid. Try again.",backToMainMarkup());return
 }
 if !confirm {s.sendOrEdit(chatID,msgID,"Change cancelled.",backToMainMarkup());return}
 if !s.cfg.AllowMutations {s.sendOrEdit(chatID,msgID,"Remote writes are disabled.",backToMainMarkup());return}
 msg,err:=changeManagedUser(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,request.Change)
 if err!=nil {
  log.Printf("[Bot] management %s failed: %v",request.Change.Kind,err)
  s.sendOrEdit(chatID,msgID,"❌ Change failed:\n"+err.Error(),backToMainMarkup())
  return
 }
 // Traffic rename is performed by changeDirectoryUser under the same config lock.
 s.sendOrEdit(chatID,msgID,"✅ Completed:\n"+msg+"\n\nIf the UUID or inbound assignment changed, import the updated client link.",backToMainMarkup())
}

func (s *BotService) handleUserManagementCommand(msg *tgbotapi.Message) bool {
 cmd:=msg.Command()
 if cmd!="adduser" && cmd!="renameuser" && cmd!="rotateuser" && cmd!="removeuser" && cmd!="assignuser" && cmd!="unassignuser" && cmd!="disableuser" && cmd!="enableuser" {return false}
 parts:=strings.Fields(msg.CommandArguments())
 change:=UserChange{}
 switch cmd {
 case "adduser":
  if len(parts)<1 || len(parts)>2 {s.sendAlert(msg.Chat.ID,"Usage: /adduser <user> [inbound-tag | all]");return true}
  change=UserChange{Kind:"add",User:parts[0]}
  if len(parts)==2 {change.Node=parts[1]}
 case "renameuser":
  if len(parts)!=2{s.sendAlert(msg.Chat.ID,"Usage: /renameuser <old-name> <new-name>");return true}
  change=UserChange{Kind:"rename",User:parts[0],NewName:parts[1]}
 case "disableuser", "enableuser":
  if len(parts)!=1 {s.sendAlert(msg.Chat.ID,"Usage: /"+cmd+" <user>");return true}
  kind:="disable";if cmd=="enableuser"{kind="enable"}
  change=UserChange{Kind:kind,User:parts[0]}
 case "rotateuser":
  if len(parts)!=1{s.sendAlert(msg.Chat.ID,"Usage: /rotateuser <user>");return true}
  change=UserChange{Kind:"rotate",User:parts[0]}
 case "removeuser":
  if len(parts)!=1{s.sendAlert(msg.Chat.ID,"Usage: /removeuser <user>");return true}
  change=UserChange{Kind:"delete",User:parts[0]}
 case "assignuser", "unassignuser":
  if len(parts)!=2{s.sendAlert(msg.Chat.ID,"Usage: /"+cmd+" <user> <inbound-tag>");return true}
  kind:="assign";if cmd=="unassignuser"{kind="unassign"}
  change=UserChange{Kind:kind,User:parts[0],Node:parts[1]}
 }
 s.offerUserChange(msg.Chat.ID,msg.From.ID,0,change)
 return true
}

func lookupManagedUserEmail(nodes []InboundDetails,key string) string {
 for _,email:=range sortedEmails(nodes){if userCallbackKey(email)==key{return email}}
 return ""
}

func (s *BotService) sendUserManageCard(chatID int64,msgID int,key string) {
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"nodes.failure",err.Error()),backToMainMarkup());return}
 email:=lookupManagedUserByKey(s.cfg.XrayConfigPath,key)
 if email==""{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"users.empty"),backToMainMarkup());return}
 var rows [][]tgbotapi.InlineKeyboardButton
 var content strings.Builder
 content.WriteString(tr(s.cfg.Language,"account.title",email))
 if state,e:=loadTrafficStore(s.cfg.XrayConfigPath);e==nil {
  if c,ok:=state.Users[email];ok {
   content.WriteString(tr(s.cfg.Language,"account.total",FormatBytes(c.TotalUp),FormatBytes(c.TotalDown),FormatBytes(c.TotalUp+c.TotalDown)))
  }
  if ips,known:=recentOnline(state,email,time.Now());!known{
   content.WriteString(tr(s.cfg.Language,"account.unknown"))
  }else if len(ips)>0{
   content.WriteString(tr(s.cfg.Language,"account.online",len(ips)))
   for _,ip:=range ips{content.WriteString("  • "+ip.IP+"\n")}
  }else{content.WriteString(tr(s.cfg.Language,"account.offline"))}
  if stamp:=state.LastOnline[email];stamp>0 {
   content.WriteString(tr(s.cfg.Language,"account.last",time.Unix(stamp,0).Format("2006-01-02 15:04:05")))
  }
  if entries:=state.OnlineIPs[email];len(entries)>0{
   content.WriteString(tr(s.cfg.Language,"account.recent"))
   for i,ip:=range entries{if i>=3{break};if i>0{content.WriteString(", ")};content.WriteString(ip.IP)}
   content.WriteString("\n")
  }
  content.WriteString(tr(s.cfg.Language,"account.warning"))
 }else{
  content.WriteString(tr(s.cfg.Language,"account.error",e.Error()))
 }
 for _,node:=range nodes {
  present:=false
  for _,client:=range node.Clients{if client.Email==email{present=true;break}}
  mark:=tr(s.cfg.Language,"account.join");op:="a"
  if present {mark=tr(s.cfg.Language,"account.leave");op="r"}
  status:=tr(s.cfg.Language,"account.unassigned");if present{status=tr(s.cfg.Language,"account.assigned")}
  content.WriteString(fmt.Sprintf("• %s (%d): %s\n",node.Tag,node.Port,status))
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(mark+" "+truncateLabel(node.Tag,28),"manage_node:"+op+":"+key+":"+userCallbackKey(node.Tag)),
  ))
 }
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"account.refresh"),"manage_user:"+key),
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"account.export"),"user_links:"+key),
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"account.rename"),"manage_rename:"+key),
 ))
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"account.rotate"),"manage_rotate:"+key),
 ))
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"account.suspend"),"manage_disable:"+key),
 ))
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"account.delete"),"manage_delete:"+key),
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"account.list"),"menu_users"),
 ))
 content.WriteString(tr(s.cfg.Language,"account.footer"))
 s.sendOrEdit(chatID,msgID,content.String(),tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) handleManagedUserCallback(cb *tgbotapi.CallbackQuery) {
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID
 msgID:=cb.Message.MessageID
 parts:=strings.Split(cb.Data,":")
 if len(parts)<2{return}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,"Failed to load inbounds: "+err.Error());return}
 switch parts[0] {
 case "manage_user":
  s.sendUserManageCard(chatID,msgID,parts[1]);return
 case "manage_rotate","manage_delete","manage_disable":
  email:=lookupManagedUserByKey(s.cfg.XrayConfigPath,parts[1])
  if email==""{s.sendAlert(chatID,"User does not exist");return}
  kind:="rotate";if parts[0]=="manage_delete"{kind="delete"};if parts[0]=="manage_disable"{kind="disable"}
  s.offerUserChange(chatID,cb.From.ID,msgID,UserChange{Kind:kind,User:email})
 case "manage_node":
  if len(parts)!=4{return}
  email:=lookupManagedUserByKey(s.cfg.XrayConfigPath,parts[2])
  if email==""{s.sendAlert(chatID,"User does not exist");return}
  for _,node:=range nodes {
   if userCallbackKey(node.Tag)!=parts[3] {continue}
   kind:="assign";if parts[1]=="r"{kind="unassign"}else if parts[1]!="a"{return}
   s.offerUserChange(chatID,cb.From.ID,msgID,UserChange{Kind:kind,User:email,Node:node.Tag})
   return
  }
  s.sendAlert(chatID,"Inbound no longer exists")
 }
}

func (s *BotService) sendDisabledUsersCard(chatID int64,msgID int) {
 names,err:=disabledUserNames(s.cfg.XrayConfigPath)
 if err!=nil {s.sendOrEdit(chatID,msgID,"Failed to load suspended users: "+err.Error(),backToMainMarkup());return}
 rows:=[][]tgbotapi.InlineKeyboardButton{}
 for _,name:=range names {
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("▶ Restore "+truncateLabel(name,40),"manage_enable:"+userCallbackKey(name)),
  ))
 }
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData("🔙 User management","menu_users"),
 ))
 message:="⏸ Suspended users\n\nRestoring a user reinstates the original UUID and inbound assignments, then restarts Xray."
 if len(names)==0 {message="There are no suspended users."}
 s.sendOrEdit(chatID,msgID,message,tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) enableDisabledUserCallback(cb *tgbotapi.CallbackQuery) {
 if cb.Message==nil{return}
 names,err:=disabledUserNames(s.cfg.XrayConfigPath)
 if err!=nil {s.sendAlert(cb.Message.Chat.ID,"Suspended user data is corrupted");return}
 key:=strings.TrimPrefix(cb.Data,"manage_enable:")
 for _,name:=range names {
  if userCallbackKey(name)==key {
   s.offerUserChange(cb.Message.Chat.ID,cb.From.ID,cb.Message.MessageID,UserChange{Kind:"enable",User:name})
   return
  }
 }
 s.sendAlert(cb.Message.Chat.ID,"Suspended user not found. Refresh the list.")
}

func lookupManagedUserByKey(configPath,key string) string {
 names,err:=allDirectoryNames(configPath);if err!=nil{return ""}
 for _,name:=range names{if userCallbackKey(name)==key{return name}}
 return ""
}
