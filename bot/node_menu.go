package main

import (
 "crypto/rand"
 "encoding/hex"
 "log"
 "strconv"
 "strings"
 "sync"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type pendingNodeChange struct{ Change NodeChange; ChatID int64; AdminID int64; Expires time.Time }
type nodePathWizard struct {Tag string; AdminID int64; Expires time.Time; Kind string}

var pendingNodes=struct{sync.Mutex;items map[string]pendingNodeChange}{items:map[string]pendingNodeChange{}}
var nodePathWizards=struct{sync.Mutex;items map[int64]nodePathWizard}{items:map[int64]nodePathWizard{}}

func findManagedNodeByKey(nodes []InboundDetails,key string) *InboundDetails {
 var found *InboundDetails
 for i:=range nodes {
  if nodes[i].Protocol!="vless" || userCallbackKey(nodes[i].Tag)!=key {continue}
  if found!=nil {return nil} // Reject ambiguous tags/callbacks.
  found=&nodes[i]
 }
 return found
}

func maskRealityShortID(shortID,lang string) string {
 if shortID=="" {return tr(lang,"node.notconfigured")}
 if len(shortID)<=4 {return tr(lang,"node.hidden")}
 return shortID[:4]+"…"
}

func (s *BotService) sendNodeDetail(chatID int64,msgID int,key string) {
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.readfailure",err.Error()),s.backToMainMarkup());return}
 node:=findManagedNodeByKey(nodes,key)
 if node==nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.notfound"),s.backToMainMarkup());return}

 msg:=tr(s.cfg.Language,"node.detail",
  node.Tag,strings.ToUpper(node.Protocol),strings.ToUpper(node.Network),strings.ToUpper(node.Security),
  node.Port,node.SharePort(),len(node.Clients))
 rows:=[][]tgbotapi.InlineKeyboardButton{}

 if node.Security=="reality" {
  msg+=tr(s.cfg.Language,"node.realitydetail",node.SNI,maskRealityShortID(node.ShortID,s.cfg.Language))
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.edit.sni"),"node_sni:"+key)))
 }
 if node.Network=="xhttp" {
  mode:=node.XHTTPMode
  if mode=="" {mode="auto"}
  msg+=tr(s.cfg.Language,"node.xhttpdetail",node.XHTTPPath,mode)
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.edit.path"),"node_path:"+key)))
  if node.Security=="tls" {
   msg+=tr(s.cfg.Language,"node.publicnote")
   rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.edit.publicport"),"node_public_port:"+key)))
  }
  modes:=[]string{"auto","packet-up","stream-up","stream-one"}
  for i:=0;i<len(modes);i+=2 {
   buttons:=make([]tgbotapi.InlineKeyboardButton,0,2)
   for _,option:=range modes[i:i+2] {
    label:=option
    if option==mode {label="● "+option}
    buttons=append(buttons,tgbotapi.NewInlineKeyboardButtonData(label,"node_mode:"+key+":"+option))
   }
   rows=append(rows,tgbotapi.NewInlineKeyboardRow(buttons...))
  }
 }
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.backlist"),"menu_nodes")))
 s.sendOrEdit(chatID,msgID,msg,tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) offerNodeChange(chatID,adminID int64,msgID int,change NodeChange){
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.mutationsdisabled"),s.backToMainMarkup());return}
 var randKey [8]byte
 if _,err:=rand.Read(randKey[:]);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.tokenerror"));return}
 key:=hex.EncodeToString(randKey[:])
 pendingNodes.Lock()
 for token,v:=range pendingNodes.items {if time.Now().After(v.Expires){delete(pendingNodes.items,token)}}
 pendingNodes.items[key]=pendingNodeChange{Change:change,ChatID:chatID,AdminID:adminID,Expires:time.Now().Add(3*time.Minute)}
 pendingNodes.Unlock()
 note:=tr(s.cfg.Language,"node.changenote.generic")
 if change.Kind=="public_port"{note=tr(s.cfg.Language,"node.changenote.publicport")}
 if change.Kind=="reality_sni"{note=tr(s.cfg.Language,"node.changenote.sni")}
 body:=tr(s.cfg.Language,"node.changebody",change.Tag,change.Kind,change.Value,note)
 keyboard:=tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.confirm"),"node_confirm:"+key),
  tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"),"node_cancel:"+key)))
 s.sendOrEdit(chatID,msgID,body,keyboard)
}

func (s *BotService) confirmNodeChange(cb *tgbotapi.CallbackQuery,confirm bool){
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 token:=strings.TrimPrefix(strings.TrimPrefix(cb.Data,"node_confirm:"),"node_cancel:")
 pendingNodes.Lock()
 p,ok:=pendingNodes.items[token]
 if ok{delete(pendingNodes.items,token)}
 pendingNodes.Unlock()
 if !ok||p.ChatID!=chatID||p.AdminID!=cb.From.ID||time.Now().After(p.Expires){s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.expired"),s.backToMainMarkup());return}
 if !confirm{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.cancelled"),s.backToMainMarkup());return}
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.denied"),s.backToMainMarkup());return}
 var result string
 var err error
 if p.Change.Kind=="public_port"{
  port,parseErr:=strconv.Atoi(p.Change.Value)
  if parseErr!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.invalidport"));return}
  result,err=SetInboundPublicPort(s.cfg.XrayConfigPath,p.Change.Tag,port)
 }else{
  result,err=ChangeNodeConfig(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,p.Change)
 }
 if err!=nil{log.Printf("[Bot] node update failed: %v",err);s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.updatefailed",err.Error()),s.backToMainMarkup());return}
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.updatesuccess",result),s.backToMainMarkup())
}

func (s *BotService) handleNodeCallback(cb *tgbotapi.CallbackQuery){
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 parts:=strings.Split(cb.Data,":");if len(parts)<2{return}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.readfailure",err.Error()));return}
 node:=findManagedNodeByKey(nodes,parts[1])
 if node==nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.missing"));return}
 switch parts[0] {
 case "node_detail":
  s.sendNodeDetail(chatID,msgID,parts[1])
 case "node_sni":
  if node.Security!="reality"{s.sendAlert(chatID,tr(s.cfg.Language,"node.notreality"));return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.enablemutations"));return}
  nodePathWizards.Lock()
  nodePathWizards.items[chatID]=nodePathWizard{Tag:node.Tag,AdminID:cb.From.ID,Expires:time.Now().Add(5*time.Minute),Kind:"reality_sni"}
  nodePathWizards.Unlock()
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.sniprompt"),s.backToMainMarkup())
 case "node_path":
  if node.Network!="xhttp"{s.sendAlert(chatID,tr(s.cfg.Language,"node.notxhttp"));return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.enablemutations"));return}
  nodePathWizards.Lock()
  nodePathWizards.items[chatID]=nodePathWizard{Tag:node.Tag,AdminID:cb.From.ID,Expires:time.Now().Add(5*time.Minute),Kind:"path"}
  nodePathWizards.Unlock()
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.pathprompt"),s.backToMainMarkup())
 case "node_public_port":
  if node.Network!="xhttp"||node.Security!="tls"{s.sendAlert(chatID,tr(s.cfg.Language,"node.portonly"));return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.mutationsdisabled"));return}
  nodePathWizards.Lock()
  nodePathWizards.items[chatID]=nodePathWizard{Tag:node.Tag,AdminID:cb.From.ID,Expires:time.Now().Add(5*time.Minute),Kind:"public_port"}
  nodePathWizards.Unlock()
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.portprompt",node.Port),s.backToMainMarkup())
 case "node_mode":
  if node.Network!="xhttp"||len(parts)!=3{return}
  s.offerNodeChange(chatID,cb.From.ID,msgID,NodeChange{Kind:"mode",Tag:node.Tag,Value:parts[2]})
 }
}

func (s *BotService) handleNodePathInput(msg *tgbotapi.Message) bool {
 chatID:=msg.Chat.ID
 nodePathWizards.Lock()
 state,ok:=nodePathWizards.items[chatID]
 if !ok{nodePathWizards.Unlock();return false}
 if time.Now().After(state.Expires)||state.AdminID!=msg.From.ID{
  delete(nodePathWizards.items,chatID);nodePathWizards.Unlock();return false
 }
 if strings.TrimSpace(msg.Text)=="/cancel"{
  delete(nodePathWizards.items,chatID);nodePathWizards.Unlock()
  s.sendAlert(chatID,tr(s.cfg.Language,"node.cancelinput"));return true
 }
 if msg.Command()!=""{nodePathWizards.Unlock();return false}
 delete(nodePathWizards.items,chatID)
 nodePathWizards.Unlock()
 value:=strings.TrimSpace(msg.Text)
 switch state.Kind {
 case "public_port":
  port,err:=strconv.Atoi(value)
  if err!=nil||port<0||port>65535{s.sendAlert(chatID,tr(s.cfg.Language,"node.portinvalid"));return true}
 case "path":
  if !safeXHTTPPath.MatchString(value){s.sendAlert(chatID,tr(s.cfg.Language,"node.pathinvalid"));return true}
 case "reality_sni":
  if err:=validateRealitySNI(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.sniinvalid",err.Error()));return true}
 default:
  s.sendAlert(chatID,tr(s.cfg.Language,"node.unknownop"));return true
 }
 s.offerNodeChange(chatID,msg.From.ID,0,NodeChange{Kind:state.Kind,Tag:state.Tag,Value:value})
 return true
}
