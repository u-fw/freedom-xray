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

func nodeChangeFieldLabel(lang,kind string) string {
 switch kind {
 case "reality_sni": return tr(lang,"node.field.sni")
 case "reality_short_id": return tr(lang,"node.field.shortid")
 case "path": return tr(lang,"node.field.path")
 case "mode": return tr(lang,"node.field.mode")
 case "public_port": return tr(lang,"node.field.publicport")
 case "port": return tr(lang,"node.field.listenport")
 case "xhttp_host": return tr(lang,"node.field.host")
 case "xhttp_padding": return tr(lang,"node.field.padding")
 }
 return kind
}

func (s *BotService) startNodeWizard(chatID,adminID int64,msgID int,tag,kind,prompt string){
 nodePathWizards.Lock()
 nodePathWizards.items[chatID]=nodePathWizard{Tag:tag,AdminID:adminID,Expires:time.Now().Add(5*time.Minute),Kind:kind}
 nodePathWizards.Unlock()
 s.sendOrEdit(chatID,msgID,prompt,s.backToMainMarkup())
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
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.edit.shortid"),"node_short_id:"+key)))
 }
 if node.Network=="xhttp" {
  mode:=node.XHTTPMode
  if mode=="" {mode="auto"}
  host:=node.XHTTPHost
  if host=="" {host=tr(s.cfg.Language,"node.notconfigured")}
  padding:=node.XHTTPPaddingBytes
  if padding=="" {padding=tr(s.cfg.Language,"node.default")}
  msg+=tr(s.cfg.Language,"node.xhttpdetail",node.XHTTPPath,mode,host,padding)
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.edit.path"),"node_path:"+key)))
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.edit.host"),"node_xhttp_host:"+key)))
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.edit.padding"),"node_xhttp_padding:"+key)))
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
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.edit.listenport"),"node_listen_port:"+key)))
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
 switch change.Kind {
 case "public_port": note=tr(s.cfg.Language,"node.changenote.publicport")
 case "reality_sni": note=tr(s.cfg.Language,"node.changenote.sni")
 case "port": note=tr(s.cfg.Language,"node.changenote.port")
 case "xhttp_host": note=tr(s.cfg.Language,"node.changenote.host")
 case "xhttp_padding": note=tr(s.cfg.Language,"node.changenote.padding")
 case "reality_short_id": note=tr(s.cfg.Language,"node.changenote.shortid")
 }
 value:=change.Value
 if value==""{value=tr(s.cfg.Language,"node.clear")}
 body:=tr(s.cfg.Language,"node.changebody",mdCode(change.Tag),mdEsc(nodeChangeFieldLabel(s.cfg.Language,change.Kind)),mdCode(value),note)
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
  s.startNodeWizard(chatID,cb.From.ID,msgID,node.Tag,"reality_sni",tr(s.cfg.Language,"node.sniprompt"))
 case "node_short_id":
  if node.Security!="reality"{s.sendAlert(chatID,tr(s.cfg.Language,"node.notreality"));return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.enablemutations"));return}
  s.startNodeWizard(chatID,cb.From.ID,msgID,node.Tag,"reality_short_id",tr(s.cfg.Language,"node.shortidprompt",mdCode(maskRealityShortID(node.ShortID,s.cfg.Language))))
 case "node_path":
  if node.Network!="xhttp"{s.sendAlert(chatID,tr(s.cfg.Language,"node.notxhttp"));return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.enablemutations"));return}
  s.startNodeWizard(chatID,cb.From.ID,msgID,node.Tag,"path",tr(s.cfg.Language,"node.pathprompt"))
 case "node_xhttp_host":
  if node.Network!="xhttp"{s.sendAlert(chatID,tr(s.cfg.Language,"node.notxhttp"));return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.enablemutations"));return}
  current:=node.XHTTPHost
  if current==""{current=tr(s.cfg.Language,"node.notconfigured")}
  s.startNodeWizard(chatID,cb.From.ID,msgID,node.Tag,"xhttp_host",tr(s.cfg.Language,"node.hostprompt",mdCode(current)))
 case "node_xhttp_padding":
  if node.Network!="xhttp"{s.sendAlert(chatID,tr(s.cfg.Language,"node.notxhttp"));return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.enablemutations"));return}
  current:=node.XHTTPPaddingBytes
  if current==""{current=tr(s.cfg.Language,"node.default")}
  s.startNodeWizard(chatID,cb.From.ID,msgID,node.Tag,"xhttp_padding",tr(s.cfg.Language,"node.paddingprompt",mdCode(current)))
 case "node_public_port":
  if node.Network!="xhttp"||node.Security!="tls"{s.sendAlert(chatID,tr(s.cfg.Language,"node.portonly"));return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.mutationsdisabled"));return}
  s.startNodeWizard(chatID,cb.From.ID,msgID,node.Tag,"public_port",tr(s.cfg.Language,"node.portprompt",node.Port))
 case "node_listen_port":
  if !s.cfg.AllowMutations{s.sendAlert(chatID,tr(s.cfg.Language,"node.enablemutations"));return}
  s.startNodeWizard(chatID,cb.From.ID,msgID,node.Tag,"port",tr(s.cfg.Language,"node.listenportprompt",node.Port))
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
 case "port":
  if _,err:=validateListenPort(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.listenportinvalid"));return true}
 case "path":
  if !safeXHTTPPath.MatchString(value){s.sendAlert(chatID,tr(s.cfg.Language,"node.pathinvalid"));return true}
 case "reality_sni":
  if err:=validateRealitySNI(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.sniinvalid",err.Error()));return true}
 case "reality_short_id":
  if value=="-"{value=""}
  if err:=validateRealityShortID(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.shortidinvalid",err.Error()));return true}
 case "xhttp_host":
  if value=="-"{value=""}
  if err:=validateXHTTPHost(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.hostinvalid",err.Error()));return true}
 case "xhttp_padding":
  if err:=validateXHTTPPadding(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.paddinginvalid",err.Error()));return true}
 default:
  s.sendAlert(chatID,tr(s.cfg.Language,"node.unknownop"));return true
 }
 s.offerNodeChange(chatID,msg.From.ID,0,NodeChange{Kind:state.Kind,Tag:state.Tag,Value:value})
 return true
}
