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
 prompt:="Enter a new username (1–48 characters: letters, digits, _, ., @, -), or send /cancel."
 if stage=="rename"{prompt=fmt.Sprintf("Enter a new name for user %s, or send /cancel.",user)}
 s.sendOrEdit(chatID,msgID,prompt,backToMainMarkup())
}

func (s *BotService) handleWizardMessage(msg *tgbotapi.Message) bool {
 chatID:=msg.Chat.ID
 if msg.Command()=="cancel" {
  managementWizards.Lock();delete(managementWizards.states,chatID);managementWizards.Unlock()
  s.sendAlert(chatID,"Input cancelled.")
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
  s.sendAlert(chatID,"Invalid name: use 1–48 ASCII letters, digits, _, ., @, or -. Reopen user management.")
  return true
 }
 if state.Stage=="rename" {
  s.offerUserChange(chatID,msg.From.ID,0,UserChange{Kind:"rename",User:state.User,NewName:input})
  return true
 }
 if state.Stage!="name"{return true}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,"Failed to load inbounds: "+err.Error());return true}
 var rows [][]tgbotapi.InlineKeyboardButton
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Create user only (no inbound access)","manage_scope:unbound")))
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Assign all inbounds (not recommended)","manage_scope:all")))
 for _,n:=range nodes {
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(truncateLabel(n.Tag,48),"manage_scope:"+userCallbackKey(n.Tag)),
  ))
 }
 managementWizards.Lock()
 managementWizards.states[chatID]=managementWizard{AdminID:msg.From.ID,Expires:time.Now().Add(5*time.Minute),Stage:"scope",User:input}
 managementWizards.Unlock()
 s.sendOrEdit(chatID,0,"Create independent user "+input+". Save the identity without node access and assign inbounds from user details later.",tgbotapi.NewInlineKeyboardMarkup(rows...))
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
 if !ok{s.sendOrEdit(chatID,msgID,"Operation expired. Add the user again.",backToMainMarkup());return}
 key:=strings.TrimPrefix(cb.Data,"manage_scope:")
 target:=""
 if key=="all"{target="all"}
 if key!="all"&&key!="unbound" {
  nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
  if err!=nil{s.sendAlert(chatID,"Cannot read inbounds");return}
  target=""
  for _,n:=range nodes {if userCallbackKey(n.Tag)==key{target=n.Tag;break}}
  if target==""{s.sendAlert(chatID,"Selected inbound changed. Retry the operation.");return}
 }
 s.offerUserChange(chatID,cb.From.ID,msgID,UserChange{Kind:"add",User:state.User,Node:target})
}
