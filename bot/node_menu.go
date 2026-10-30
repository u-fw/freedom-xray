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

type pendingNodeChange struct{ Change NodeChange; ChatID int64; AdminID int64; Expires time.Time }
type nodePathWizard struct {Tag string; AdminID int64; Expires time.Time}
var pendingNodes=struct{sync.Mutex;items map[string]pendingNodeChange}{items:map[string]pendingNodeChange{}}
var nodePathWizards=struct{sync.Mutex;items map[int64]nodePathWizard}{items:map[int64]nodePathWizard{}}

func findXHTTPNodeByKey(nodes []InboundDetails,key string) *InboundDetails {
 for i:=range nodes {if nodes[i].Network=="xhttp"&&userCallbackKey(nodes[i].Tag)==key{return &nodes[i]}}
 return nil
}
func (s *BotService) sendNodeDetail(chatID int64,msgID int,key string){
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath);if err!=nil{s.sendOrEdit(chatID,msgID,"读取节点失败："+err.Error(),backToMainMarkup());return}
 node:=findXHTTPNodeByKey(nodes,key);if node==nil{s.sendOrEdit(chatID,msgID,"节点不存在或并非 XHTTP",backToMainMarkup());return}
 mode:=node.XHTTPMode;if mode==""{mode="auto"}
 msg:=fmt.Sprintf("XHTTP 节点：%s\n端口：%d\n安全层：%s\nPath：%s\nMode：%s\n\n修改 Path/Mode 会让已导入客户端需要同步配置。",node.Tag,node.Port,node.Security,node.XHTTPPath,mode)
 rows:=[][]tgbotapi.InlineKeyboardButton{
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("✏️ 修改 Path","node_path:"+key)),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("Mode: auto","node_mode:"+key+":auto"),tgbotapi.NewInlineKeyboardButtonData("packet-up","node_mode:"+key+":packet-up")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("stream-up","node_mode:"+key+":stream-up"),tgbotapi.NewInlineKeyboardButtonData("stream-one","node_mode:"+key+":stream-one")),
  tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔙 节点列表","menu_nodes")),
 }
 s.sendOrEdit(chatID,msgID,msg,tgbotapi.NewInlineKeyboardMarkup(rows...))
}
func (s *BotService) offerNodeChange(chatID,adminID int64,msgID int,change NodeChange){
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,"远程修改未授权，请先在 VPS 设置 allow_mutations。",backToMainMarkup());return}
 var randKey [8]byte;if _,err:=rand.Read(randKey[:]);err!=nil{s.sendAlert(chatID,"无法生成操作令牌");return}
 key:=hex.EncodeToString(randKey[:])
 pendingNodes.Lock()
 for token,v:=range pendingNodes.items{if time.Now().After(v.Expires){delete(pendingNodes.items,token)}}
 pendingNodes.items[key]=pendingNodeChange{Change:change,ChatID:chatID,AdminID:adminID,Expires:time.Now().Add(3*time.Minute)}
 pendingNodes.Unlock()
 text:=fmt.Sprintf("确认修改 XHTTP 节点 %s 的 %s 为 %s？\n\n将先校验新配置，再重启 Xray。旧客户端配置可能需要同步。",change.Tag,change.Kind,change.Value)
 keyboard:=tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("✅ 确认","node_confirm:"+key),tgbotapi.NewInlineKeyboardButtonData("取消","node_cancel:"+key)))
 s.sendOrEdit(chatID,msgID,text,keyboard)
}
func (s *BotService) confirmNodeChange(cb *tgbotapi.CallbackQuery,confirm bool){
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 token:=strings.TrimPrefix(strings.TrimPrefix(cb.Data,"node_confirm:"),"node_cancel:")
 pendingNodes.Lock();p,ok:=pendingNodes.items[token];if ok{delete(pendingNodes.items,token)};pendingNodes.Unlock()
 if !ok||p.ChatID!=chatID||p.AdminID!=cb.From.ID||time.Now().After(p.Expires){s.sendOrEdit(chatID,msgID,"操作已过期",backToMainMarkup());return}
 if !confirm{s.sendOrEdit(chatID,msgID,"已取消",backToMainMarkup());return}
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,"修改权限已关闭",backToMainMarkup());return}
 result,err:=ChangeNodeConfig(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,p.Change)
 if err!=nil{log.Printf("[Bot] node update failed: %v",err);s.sendOrEdit(chatID,msgID,"修改失败："+err.Error(),backToMainMarkup());return}
 s.sendOrEdit(chatID,msgID,"修改成功："+result,backToMainMarkup())
}
func (s *BotService) handleNodeCallback(cb *tgbotapi.CallbackQuery){
 if cb.Message==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 parts:=strings.Split(cb.Data,":");if len(parts)<2{return}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath);if err!=nil{s.sendAlert(chatID,"节点读取失败");return}
 node:=findXHTTPNodeByKey(nodes,parts[1]);if node==nil{s.sendAlert(chatID,"找不到 XHTTP 节点");return}
 switch parts[0]{
 case "node_detail":s.sendNodeDetail(chatID,msgID,parts[1])
 case "node_path":
  if !s.cfg.AllowMutations{s.sendAlert(chatID,"需要先开启远程修改权限");return}
  nodePathWizards.Lock();nodePathWizards.items[chatID]=nodePathWizard{Tag:node.Tag,AdminID:cb.From.ID,Expires:time.Now().Add(5*time.Minute)};nodePathWizards.Unlock()
  s.sendOrEdit(chatID,msgID,"请输入新 XHTTP Path（例如 /stream）；发送 /cancel 取消。",backToMainMarkup())
 case "node_mode":
  if len(parts)!=3{return}
  s.offerNodeChange(chatID,cb.From.ID,msgID,NodeChange{Kind:"mode",Tag:node.Tag,Value:parts[2]})
 }
}
func (s *BotService) handleNodePathInput(msg *tgbotapi.Message) bool {
 chatID:=msg.Chat.ID
 nodePathWizards.Lock();state,ok:=nodePathWizards.items[chatID]
 if !ok{nodePathWizards.Unlock();return false}
 if time.Now().After(state.Expires)||state.AdminID!=msg.From.ID{delete(nodePathWizards.items,chatID);nodePathWizards.Unlock();return false}
 if strings.TrimSpace(msg.Text)=="/cancel" {delete(nodePathWizards.items,chatID);nodePathWizards.Unlock();s.sendAlert(chatID,"已取消 Path 修改");return true}
 if msg.Command()!=""{nodePathWizards.Unlock();return false}
 delete(nodePathWizards.items,chatID);nodePathWizards.Unlock()
 path:=strings.TrimSpace(msg.Text)
 if !safeXHTTPPath.MatchString(path){s.sendAlert(chatID,"Path 无效。必须以 / 开始，仅允许字母、数字、/、_、-、.、~，最多 128 字符。");return true}
 s.offerNodeChange(chatID,msg.From.ID,0,NodeChange{Kind:"path",Tag:state.Tag,Value:path})
 return true
}
