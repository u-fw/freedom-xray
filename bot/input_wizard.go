package main

import (
 "fmt"
 "strings"
 "sync"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type managementWizard struct {
 AdminID int64
 Expires time.Time
 Stage string
 User string
}

var managementWizards = struct{
 sync.Mutex
 states map[int64]managementWizard
}{states:make(map[int64]managementWizard)}

func (s *BotService) beginUserWizard(chatID,adminID int64,msgID int,stage,user string) {
 managementWizards.Lock()
 managementWizards.states[chatID]=managementWizard{AdminID:adminID,Expires:time.Now().Add(5*time.Minute),Stage:stage,User:user}
 managementWizards.Unlock()
 prompt:="输入新用户名称（1–48 位字母、数字、_、.、@、-）；或发送 /cancel 取消。"
 if stage=="rename"{prompt=fmt.Sprintf("请输入用户 %s 的新名称；或发送 /cancel 取消。",user)}
 s.sendOrEdit(chatID,msgID,prompt,backToMainMarkup())
}

func (s *BotService) handleWizardMessage(msg *tgbotapi.Message) bool {
 chatID:=msg.Chat.ID
 if msg.Command()=="cancel" {
  managementWizards.Lock();delete(managementWizards.states,chatID);managementWizards.Unlock()
  s.sendAlert(chatID,"已取消输入。")
  return true
 }
 managementWizards.Lock()
 state,ok:=managementWizards.states[chatID]
 if ok && (time.Now().After(state.Expires)||msg.From.ID!=state.AdminID){delete(managementWizards.states,chatID);ok=false}
 if ok && msg.Command()=="" {delete(managementWizards.states,chatID)}
 managementWizards.Unlock()
 if !ok||msg.Command()!=""{return false}
 input:=strings.TrimSpace(msg.Text)
 if !managedUserName.MatchString(input) {
  s.sendAlert(chatID,"名称无效：必须是 1–48 位英文字母、数字、_、.、@ 或 -。请重新选择用户管理入口。")
  return true
 }
 if state.Stage=="rename" {
  s.offerUserChange(chatID,msg.From.ID,0,UserChange{Kind:"rename",User:state.User,NewName:input})
  return true
 }
 if state.Stage!="name"{return true}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,"读取节点失败: "+err.Error());return true}
 var rows [][]tgbotapi.InlineKeyboardButton
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("所有节点","manage_scope:all")))
 for _,n:=range nodes {
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(truncateLabel(n.Tag,48),"manage_scope:"+userCallbackKey(n.Tag)),
  ))
 }
 managementWizards.Lock()
 managementWizards.states[chatID]=managementWizard{AdminID:msg.From.ID,Expires:time.Now().Add(5*time.Minute),Stage:"scope",User:input}
 managementWizards.Unlock()
 s.sendOrEdit(chatID,0,"新增用户 "+input+"：请选择要分配的节点。",tgbotapi.NewInlineKeyboardMarkup(rows...))
 return true
}

func (s *BotService) handleWizardScope(cb *tgbotapi.CallbackQuery) {
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 managementWizards.Lock()
 state,ok:=managementWizards.states[chatID]
 if ok && state.AdminID==cb.From.ID && state.Stage=="scope" && time.Now().Before(state.Expires) {
  delete(managementWizards.states,chatID)
 }else{ok=false}
 managementWizards.Unlock()
 if !ok{s.sendOrEdit(chatID,msgID,"操作已过期，请重新添加用户。",backToMainMarkup());return}
 key:=strings.TrimPrefix(cb.Data,"manage_scope:")
 target:="all"
 if key!="all" {
  nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
  if err!=nil{s.sendAlert(chatID,"无法读取节点");return}
  target=""
  for _,n:=range nodes {if userCallbackKey(n.Tag)==key{target=n.Tag;break}}
  if target==""{s.sendAlert(chatID,"指定节点已变化，请重新操作。");return}
 }
 s.offerUserChange(chatID,cb.From.ID,msgID,UserChange{Kind:"add",User:state.User,Node:target})
}
