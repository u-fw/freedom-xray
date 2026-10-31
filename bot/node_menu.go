package main

import (
 "crypto/rand"
 "encoding/hex"
 "fmt"
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

func maskRealityShortID(shortID string) string {
 if shortID=="" {return "Not configured"}
 if len(shortID)<=4 {return "Configured (hidden)"}
 return shortID[:4]+"…"
}

func (s *BotService) sendNodeDetail(chatID int64,msgID int,key string) {
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendOrEdit(chatID,msgID,"Unable to read inbound: "+err.Error(),backToMainMarkup());return}
 node:=findManagedNodeByKey(nodes,key)
 if node==nil{s.sendOrEdit(chatID,msgID,"Inbound not found or duplicate tag. Refresh the list.",backToMainMarkup());return}

 msg:=fmt.Sprintf("Inbound: %s\nProtocol: %s / %s / %s\nListen port: %d\nClient port: %d\nAssigned users: %d",
  node.Tag,strings.ToUpper(node.Protocol),strings.ToUpper(node.Network),strings.ToUpper(node.Security),
  node.Port,node.SharePort(),len(node.Clients))
 rows:=[][]tgbotapi.InlineKeyboardButton{}

 if node.Security=="reality" {
  msg+=fmt.Sprintf("\nREALITY SNI: %s\nShort ID: %s\n\nSecrets and ML-DSA seeds are never shown in Telegram.",node.SNI,maskRealityShortID(node.ShortID))
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("✏️ Edit REALITY SNI","node_sni:"+key)))
 }
 if node.Network=="xhttp" {
  mode:=node.XHTTPMode
  if mode=="" {mode="auto"}
  msg+=fmt.Sprintf("\nXHTTP Path：%s\nXHTTP Mode：%s",node.XHTTPPath,mode)
  rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("✏️ Edit path","node_path:"+key)))
  if node.Security=="tls" {
   msg+="\n\nThe public port affects exports only; Cloudflare Origin Rules are not modified."
   rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🌐 Public port (Origin Rule)","node_public_port:"+key)))
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
 rows=append(rows,tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔙 Inbound list","menu_nodes")))
 s.sendOrEdit(chatID,msgID,msg,tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (s *BotService) offerNodeChange(chatID,adminID int64,msgID int,change NodeChange){
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,"Remote mutations are disabled. Enable allow_mutations on the VPS.",backToMainMarkup());return}
 var randKey [8]byte
 if _,err:=rand.Read(randKey[:]);err!=nil{s.sendAlert(chatID,"Unable to generate operation token");return}
 key:=hex.EncodeToString(randKey[:])
 pendingNodes.Lock()
 for token,v:=range pendingNodes.items {if time.Now().After(v.Expires){delete(pendingNodes.items,token)}}
 pendingNodes.items[key]=pendingNodeChange{Change:change,ChatID:chatID,AdminID:adminID,Expires:time.Now().Add(3*time.Minute)}
 pendingNodes.Unlock()
 note:="New configuration will be validated and Xray restarted; clients may require updated settings."
 if change.Kind=="public_port"{note="Only export metadata changes; the listener, Cloudflare rules and Xray process remain untouched. Verify your Origin Rule first."}
 if change.Kind=="reality_sni"{note="Updates REALITY serverNames and matching default port-443 targets while preserving custom targets. Clients must update SNI."}
 body:=fmt.Sprintf("Change %s setting %s to %s?\n\n%s",change.Tag,change.Kind,change.Value,note)
 keyboard:=tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
  tgbotapi.NewInlineKeyboardButtonData("✅ Confirm","node_confirm:"+key),
  tgbotapi.NewInlineKeyboardButtonData("Cancel","node_cancel:"+key)))
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
 if !ok||p.ChatID!=chatID||p.AdminID!=cb.From.ID||time.Now().After(p.Expires){s.sendOrEdit(chatID,msgID,"Operation expired",backToMainMarkup());return}
 if !confirm{s.sendOrEdit(chatID,msgID,"Cancelled",backToMainMarkup());return}
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,"Modification permission disabled",backToMainMarkup());return}
 var result string
 var err error
 if p.Change.Kind=="public_port"{
  port,parseErr:=strconv.Atoi(p.Change.Value)
  if parseErr!=nil{s.sendAlert(chatID,"Invalid port");return}
  result,err=SetInboundPublicPort(s.cfg.XrayConfigPath,p.Change.Tag,port)
 }else{
  result,err=ChangeNodeConfig(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,p.Change)
 }
 if err!=nil{log.Printf("[Bot] node update failed: %v",err);s.sendOrEdit(chatID,msgID,"Update failed: "+err.Error(),backToMainMarkup());return}
 s.sendOrEdit(chatID,msgID,"Update successful: "+result,backToMainMarkup())
}

func (s *BotService) handleNodeCallback(cb *tgbotapi.CallbackQuery){
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 parts:=strings.Split(cb.Data,":");if len(parts)<2{return}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,"Unable to read inbound: "+err.Error());return}
 node:=findManagedNodeByKey(nodes,parts[1])
 if node==nil{s.sendAlert(chatID,"Inbound missing or duplicate tag");return}
 switch parts[0] {
 case "node_detail":
  s.sendNodeDetail(chatID,msgID,parts[1])
 case "node_sni":
  if node.Security!="reality"{s.sendAlert(chatID,"This inbound does not use REALITY");return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,"Enable remote mutation permission first");return}
  nodePathWizards.Lock()
  nodePathWizards.items[chatID]=nodePathWizard{Tag:node.Tag,AdminID:cb.From.ID,Expires:time.Now().Add(5*time.Minute),Kind:"reality_sni"}
  nodePathWizards.Unlock()
  s.sendOrEdit(chatID,msgID,"Enter the new REALITY SNI (DNS hostname without port); send /cancel to cancel.",backToMainMarkup())
 case "node_path":
  if node.Network!="xhttp"{s.sendAlert(chatID,"This inbound does not use XHTTP");return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,"Enable remote mutation permission first");return}
  nodePathWizards.Lock()
  nodePathWizards.items[chatID]=nodePathWizard{Tag:node.Tag,AdminID:cb.From.ID,Expires:time.Now().Add(5*time.Minute),Kind:"path"}
  nodePathWizards.Unlock()
  s.sendOrEdit(chatID,msgID,"Enter the new XHTTP path (for example /stream); send /cancel to cancel.",backToMainMarkup())
 case "node_public_port":
  if node.Network!="xhttp"||node.Security!="tls"{s.sendAlert(chatID,"Only XHTTP + TLS is supported");return}
  if !s.cfg.AllowMutations{s.sendAlert(chatID,"Remote configuration changes are disabled");return}
  nodePathWizards.Lock()
  nodePathWizards.items[chatID]=nodePathWizard{Tag:node.Tag,AdminID:cb.From.ID,Expires:time.Now().Add(5*time.Minute),Kind:"public_port"}
  nodePathWizards.Unlock()
  s.sendOrEdit(chatID,msgID,fmt.Sprintf("Listener port %d. Enter the public HTTPS port (for example 443), or 0 to clear. Configure Cloudflare Origin Rules → Destination Port first.",node.Port),backToMainMarkup())
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
  s.sendAlert(chatID,"Inbound modification cancelled");return true
 }
 if msg.Command()!=""{nodePathWizards.Unlock();return false}
 delete(nodePathWizards.items,chatID)
 nodePathWizards.Unlock()
 value:=strings.TrimSpace(msg.Text)
 switch state.Kind {
 case "public_port":
  port,err:=strconv.Atoi(value)
  if err!=nil||port<0||port>65535{s.sendAlert(chatID,"Port must be 0 (clear) or 1–65535");return true}
 case "path":
  if !safeXHTTPPath.MatchString(value){s.sendAlert(chatID,"Invalid path: must begin with /, contain only letters, digits, /, _, -, ., or ~, and be at most 128 characters.");return true}
 case "reality_sni":
  if err:=validateRealitySNI(value);err!=nil{s.sendAlert(chatID,"Invalid SNI: "+err.Error());return true}
 default:
  s.sendAlert(chatID,"Unknown inbound operation");return true
 }
 s.offerNodeChange(chatID,msg.From.ID,0,NodeChange{Kind:state.Kind,Tag:state.Tag,Value:value})
 return true
}
