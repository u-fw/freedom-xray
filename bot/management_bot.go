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
  s.sendOrEdit(chatID,messageID,"🔒 远程配置修改尚未启用。请在 VPS 的 freedom → Telegram Bot 配置中显式开启 allow_mutations。只读操作仍可使用。",backToMainMarkup())
  return
 }
 var token [8]byte
 if _,err:=rand.Read(token[:]);err!=nil {s.sendAlert(chatID,"随机数生成失败: "+err.Error());return}
 key:=hex.EncodeToString(token[:])
 pendingUserChanges.Lock()
 now:=time.Now()
 for k,v:=range pendingUserChanges.items {if now.After(v.ExpiresAt){delete(pendingUserChanges.items,k)}}
 pendingUserChanges.items[key]=pendingUserChange{Change:change,ChatID:chatID,AdminID:adminID,ExpiresAt:now.Add(3*time.Minute)}
 pendingUserChanges.Unlock()

 var description string
 switch change.Kind {
 case "add": description=fmt.Sprintf("新增用户 %s (节点: %s)",change.User,scopeLabel(change.Node))
 case "rename": description=fmt.Sprintf("用户 %s 改名为 %s",change.User,change.NewName)
 case "rotate": description=fmt.Sprintf("轮换 %s 的 UUID；其旧链接将立即失效",change.User)
 case "delete": description=fmt.Sprintf("删除 %s 在所有入站的账号；链接立即失效",change.User)
 case "assign": description=fmt.Sprintf("将 %s 加入节点 %s",change.User,change.Node)
 case "unassign": description=fmt.Sprintf("从节点 %s 移除 %s",change.Node,change.User)
 default: s.sendAlert(chatID,"未知修改操作");return
 }
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData("✅ 确认修改","manage_confirm:"+key),
   tgbotapi.NewInlineKeyboardButtonData("取消","manage_cancel:"+key),
  ),
 )
 s.sendOrEdit(chatID,messageID,"⚠️ 请确认：\n"+description+"\n\n操作有效期 3 分钟；将校验配置并重启 Xray，失败则恢复旧配置。",markup)
}

func scopeLabel(node string) string {if node==""||node=="all"{return "全部入站"};return node}

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
  s.sendOrEdit(chatID,msgID,"操作已过期或无效，请重新发起。",backToMainMarkup());return
 }
 if !confirm {s.sendOrEdit(chatID,msgID,"已取消修改。",backToMainMarkup());return}
 if !s.cfg.AllowMutations {s.sendOrEdit(chatID,msgID,"远程写入已关闭。",backToMainMarkup());return}
 msg,err:=ChangeUserConfig(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,request.Change)
 if err!=nil {
  log.Printf("[Bot] management %s failed: %v",request.Change.Kind,err)
  s.sendOrEdit(chatID,msgID,"❌ 修改失败：\n"+err.Error(),backToMainMarkup())
  return
 }
 // Names and traffic samples are associated by email. Keep no stale stats for renamed users.
 if request.Change.Kind=="rename" {if err:=renameTrafficStore(s.cfg.XrayConfigPath,request.Change.User,request.Change.NewName);err!=nil {
  log.Printf("[Bot] traffic store rename failed: %v",err)
 }}
 s.sendOrEdit(chatID,msgID,"✅ 已完成：\n"+msg+"\n\nXray 已通过配置测试并恢复运行；旧链接可能需要重新导入。",backToMainMarkup())
}

func (s *BotService) handleUserManagementCommand(msg *tgbotapi.Message) bool {
 cmd:=msg.Command()
 if cmd!="adduser" && cmd!="renameuser" && cmd!="rotateuser" && cmd!="removeuser" && cmd!="assignuser" && cmd!="unassignuser" {return false}
 parts:=strings.Fields(msg.CommandArguments())
 change:=UserChange{}
 switch cmd {
 case "adduser":
  if len(parts)<1 || len(parts)>2 {s.sendAlert(msg.Chat.ID,"用法: /adduser <用户标识> [节点 tag | all]");return true}
  change=UserChange{Kind:"add",User:parts[0],Node:"all"}
  if len(parts)==2 {change.Node=parts[1]}
 case "renameuser":
  if len(parts)!=2{s.sendAlert(msg.Chat.ID,"用法: /renameuser <旧名称> <新名称>");return true}
  change=UserChange{Kind:"rename",User:parts[0],NewName:parts[1]}
 case "rotateuser":
  if len(parts)!=1{s.sendAlert(msg.Chat.ID,"用法: /rotateuser <用户名>");return true}
  change=UserChange{Kind:"rotate",User:parts[0]}
 case "removeuser":
  if len(parts)!=1{s.sendAlert(msg.Chat.ID,"用法: /removeuser <用户名>");return true}
  change=UserChange{Kind:"delete",User:parts[0]}
 case "assignuser", "unassignuser":
  if len(parts)!=2{s.sendAlert(msg.Chat.ID,"用法: /"+cmd+" <用户名> <节点tag>");return true}
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
 if err!=nil{s.sendOrEdit(chatID,msgID,"无法加载节点: "+err.Error(),backToMainMarkup());return}
 email:=lookupManagedUserEmail(nodes,key)
 if email==""{s.sendOrEdit(chatID,msgID,"用户已不存在，请刷新。",backToMainMarkup());return}
 var rows [][]tgbotapi.InlineKeyboardButton
 var content strings.Builder
 content.WriteString("👤 用户管理: "+email+"\n\n")
 for _,node:=range nodes {
  present:=false
  for _,client:=range node.Clients{if client.Email==email{present=true;break}}
  mark:="➕ 加入";op:="a"
  if present {mark="➖ 移除";op="r"}
  content.WriteString(fmt.Sprintf("• %s (%d): %v\n",node.Tag,node.Port,present))
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(mark+" "+truncateLabel(node.Tag,28),"manage_node:"+op+":"+key+":"+userCallbackKey(node.Tag)),
  ))
 }
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData("🔗 导出链接","user_links:"+key),
  tgbotapi.NewInlineKeyboardButtonData("✏️ 改名","manage_rename:"+key),
 ))
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData("🔐 重置 UUID","manage_rotate:"+key),
 ))
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData("🗑 删除用户","manage_delete:"+key),
  tgbotapi.NewInlineKeyboardButtonData("🔙 用户列表","menu_users"),
 ))
 content.WriteString("\n新增用户：/adduser <名称> [节点标签]\n改名：/renameuser <旧名称> <新名称>")
 s.sendOrEdit(chatID,msgID,content.String(),tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) handleManagedUserCallback(cb *tgbotapi.CallbackQuery) {
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID
 msgID:=cb.Message.MessageID
 parts:=strings.Split(cb.Data,":")
 if len(parts)<2{return}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,"无法加载节点: "+err.Error());return}
 switch parts[0] {
 case "manage_user":
  s.sendUserManageCard(chatID,msgID,parts[1]);return
 case "manage_rotate","manage_delete":
  email:=lookupManagedUserEmail(nodes,parts[1])
  if email==""{s.sendAlert(chatID,"用户已不存在");return}
  kind:="rotate";if parts[0]=="manage_delete"{kind="delete"}
  s.offerUserChange(chatID,cb.From.ID,msgID,UserChange{Kind:kind,User:email})
 case "manage_node":
  if len(parts)!=4{return}
  email:=lookupManagedUserEmail(nodes,parts[2])
  if email==""{s.sendAlert(chatID,"用户已不存在");return}
  for _,node:=range nodes {
   if userCallbackKey(node.Tag)!=parts[3] {continue}
   kind:="assign";if parts[1]=="r"{kind="unassign"}else if parts[1]!="a"{return}
   s.offerUserChange(chatID,cb.From.ID,msgID,UserChange{Kind:kind,User:email,Node:node.Tag})
   return
  }
  s.sendAlert(chatID,"节点已不存在")
 }
}
