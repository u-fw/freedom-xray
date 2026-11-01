package main

import (
 "crypto/rand"
 "encoding/hex"
 "errors"
 "fmt"
 "log"
 "path/filepath"
 "strconv"
 "strings"
 "sync"
 "time"

 tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type nodeCreateState struct {
 AdminID int64
 Expires time.Time
 Template NodeTemplate
 Step string
 Params NodeTemplateParams
 ClientEncryption string
 CertSource string
}

type pendingNodeCreate struct {
 ChatID int64
 AdminID int64
 Expires time.Time
 Request NodeCreateRequest
}

type pendingNodeDelete struct {
 ChatID int64
 AdminID int64
 Expires time.Time
 Tag string
 ETag string
}

var nodeCreateStates=struct{
 sync.Mutex
 items map[int64]nodeCreateState
}{items:map[int64]nodeCreateState{}}
var pendingNodeCreates=struct{
 sync.Mutex
 items map[string]pendingNodeCreate
}{items:map[string]pendingNodeCreate{}}
var pendingNodeDeletes=struct{
 sync.Mutex
 items map[string]pendingNodeDelete
}{items:map[string]pendingNodeDelete{}}

func defaultPortForTemplate(template NodeTemplate) int {
 switch template {
 case NodeTemplateTCPReality:return 443
 case NodeTemplateXHTTPReality:return 8443
 case NodeTemplateXHTTPTLS:return 2053
 }
 return 443
}

func defaultTagForTemplate(template NodeTemplate,port int) string {
 switch template {
 case NodeTemplateTCPReality:return fmt.Sprintf("TCP-REALITY-%d",port)
 case NodeTemplateXHTTPReality:return fmt.Sprintf("XHTTP-REALITY-%d",port)
 case NodeTemplateXHTTPTLS:return fmt.Sprintf("XHTTP-TLS-CDN-%d",port)
 }
 return fmt.Sprintf("node-%d",port)
}

func nodeCreateDefaults(template NodeTemplate) NodeTemplateParams {
 params:=NodeTemplateParams{
  Port:defaultPortForTemplate(template),
  Mode:"auto",
  Decryption:"none",
  XHTTPPadding:defaultNodeXHTTPPadding,
  Vision:template==NodeTemplateTCPReality,
 }
 if template!=NodeTemplateXHTTPTLS{params.SNI=defaultRealitySNI}
 return params
}

func clearNodeCreateState(chatID int64) {
 nodeCreateStates.Lock()
 delete(nodeCreateStates.items,chatID)
 nodeCreateStates.Unlock()
}

func (s *BotService) beginNodeCreate(chatID, adminID int64, msgID int, template NodeTemplate) {
	clearRoutingWizard(chatID)
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "node.mutationsdisabled"), s.backToMainMarkup())
		return
	}
	if !validNodeTemplate(template) {
		s.sendAlert(chatID, tr(s.cfg.Language, "node.create.unknown"))
		return
	}
	state := nodeCreateState{
		AdminID:  adminID,
		Expires:  time.Now().Add(10 * time.Minute),
		Template: template,
		Step:     "port",
		Params:   nodeCreateDefaults(template),
	}
	nodeCreateStates.Lock()
	nodeCreateStates.items[chatID] = state
	nodeCreateStates.Unlock()
	s.promptNodeCreate(chatID, msgID, state)
}

func (s *BotService) promptNodeCreate(chatID int64,msgID int,state nodeCreateState) {
 prompt:=""
 switch state.Step {
 case "port":
  prompt=tr(s.cfg.Language,"node.create.portprompt",defaultPortForTemplate(state.Template))
 case "tag":
  prompt=tr(s.cfg.Language,"node.create.tagprompt",mdCode(defaultTagForTemplate(state.Template,state.Params.Port)))
 case "sni":
  prompt=tr(s.cfg.Language,"node.create.sniprompt",mdCode(defaultRealitySNI))
 case "domain":
  prompt=tr(s.cfg.Language,"node.create.domainprompt")
 case "path":
  prompt=tr(s.cfg.Language,"node.create.pathprompt",mdCode(state.Params.Path))
 case "mode":
  prompt=tr(s.cfg.Language,"node.create.modeprompt")
 case "encryption":
  prompt=tr(s.cfg.Language,"node.create.encprompt")
 case "mldsa":
  prompt=tr(s.cfg.Language,"node.create.mldsaprompt")
 case "cert_path":
  prompt=tr(s.cfg.Language,"node.create.certprompt")
 case "key_path":
  prompt=tr(s.cfg.Language,"node.create.keyprompt")
 default:
  prompt=tr(s.cfg.Language,"node.create.cancelled")
 }
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"),"menu_nodes"),
  ),
 )
 if state.Step=="cert_source" {
  prompt=tr(s.cfg.Language,"node.create.certsource")
  markup=tgbotapi.NewInlineKeyboardMarkup(
   tgbotapi.NewInlineKeyboardRow(
    tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.create.certself"),"node_cert_source:self"),
    tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.create.certexisting"),"node_cert_source:existing"),
   ),
   tgbotapi.NewInlineKeyboardRow(
    tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"),"menu_nodes"),
   ),
  )
 }
 s.sendOrEdit(chatID,msgID,prompt,markup)
}

func nextNodeCreateStep(state *nodeCreateState) {
 switch state.Step {
 case "port":state.Step="tag"
 case "tag":
  if state.Template==NodeTemplateXHTTPTLS{state.Step="domain"}else{state.Step="sni"}
 case "sni":
  if state.Template==NodeTemplateXHTTPReality{state.Step="path"}else{state.Step="encryption"}
 case "domain":
  state.Step="cert_source"
 case "path":state.Step="mode"
 case "mode":state.Step="encryption"
 case "encryption":
  if state.Template==NodeTemplateXHTTPTLS{state.Step="summary"}else{state.Step="mldsa"}
 case "mldsa":state.Step="summary"
 case "cert_path":state.Step="key_path"
 case "key_path":state.Step="path"
 }
}

func tagExistsInConfig(nodes []InboundDetails,tag string) bool {
 for _,node:=range nodes {
  if node.Tag==tag{return true}
 }
 return false
}

func parseYesNo(value string)(bool,bool) {
 switch strings.ToLower(strings.TrimSpace(value)) {
 case "y","yes","1","on","true":return true,true
 case "n","no","0","off","false":return false,true
 }
 return false,false
}

func (s *BotService) handleNodeCreateInput(msg *tgbotapi.Message) bool {
 chatID:=msg.Chat.ID
 nodeCreateStates.Lock()
 state,ok:=nodeCreateStates.items[chatID]
 if ok&&time.Now().After(state.Expires){delete(nodeCreateStates.items,chatID);ok=false}
 if ok&&msg.From.ID!=state.AdminID{ok=false}
 nodeCreateStates.Unlock()
 if !ok{return false}
 if strings.TrimSpace(msg.Text)=="/cancel"||msg.Command()=="cancel" {
  nodeCreateStates.Lock();delete(nodeCreateStates.items,chatID);nodeCreateStates.Unlock()
  s.sendAlert(chatID,tr(s.cfg.Language,"node.create.cancelled"))
  return true
 }
 if msg.Command()!="" {
  // Let a real command run instead of leaving a stale wizard that would
  // capture the next plain-text reply.
  nodeCreateStates.Lock();delete(nodeCreateStates.items,chatID);nodeCreateStates.Unlock()
  return false
 }
 input:=strings.TrimSpace(msg.Text)
 if input==""{return true}
 state.Expires=time.Now().Add(10*time.Minute)
	switch state.Step {
	case "port":
	 value:=input
	 if value=="-"{value=strconv.Itoa(defaultPortForTemplate(state.Template))}
	 port,err:=validateListenPort(value)
	 if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.listenportinvalid"));return true}
	 state.Params.Port=port
	case "tag":
	 value:=input
	 if value=="-"{value=defaultTagForTemplate(state.Template,state.Params.Port)}
	 if err:=validateNodeTag(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.create.taginvalid",err.Error()));return true}
	 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
	 if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.readfailure",err.Error()));return true}
	 if tagExistsInConfig(nodes,value){s.sendAlert(chatID,tr(s.cfg.Language,"node.create.tagexists",mdEsc(value)));return true}
	 state.Params.Tag=value
	case "sni","domain":
	 value:=input
	 if state.Step=="sni"&&value=="-"{value=defaultRealitySNI}
	 if state.Step=="domain"&&value=="-"{s.sendAlert(chatID,tr(s.cfg.Language,"node.create.domainrequired"));return true}
	 if err:=validateRealitySNI(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.sniinvalid",err.Error()));return true}
	 state.Params.SNI=value
	case "path":
	 value:=input
	 if value=="-"{value=state.Params.Path}
	 if value==""{s.sendAlert(chatID,tr(s.cfg.Language,"node.pathinvalid"));return true}
	 if !safeXHTTPPath.MatchString(value){s.sendAlert(chatID,tr(s.cfg.Language,"node.pathinvalid"));return true}
	 state.Params.Path=value
	case "mode":
	 value:=input
	 if value=="-"{value="auto"}
	 if err:=validateNodeXHTTPMode(value);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.create.modeinvalid"));return true}
	 state.Params.Mode=value
	case "encryption":
	 want,valid:=parseYesNo(input)
	 if !valid{s.sendAlert(chatID,tr(s.cfg.Language,"node.create.yesno"));return true}
	 if want {
	  dec,enc,err:=generateVLESSEncryptionPair()
	  if err!=nil{
	   log.Printf("[Bot] vlessenc failed: %v",err)
	   s.sendAlert(chatID,tr(s.cfg.Language,"node.create.encfailed",err.Error()))
	   return true
	  }
	  state.Params.Decryption=dec
	  state.ClientEncryption=enc
	  if state.Template!=NodeTemplateTCPReality{state.Params.Vision=true}
	 }else{
	  state.Params.Decryption="none"
	  state.ClientEncryption="none"
	  if state.Template!=NodeTemplateTCPReality{state.Params.Vision=false}
	 }
	case "mldsa":
	 want,valid:=parseYesNo(input)
	 if !valid{s.sendAlert(chatID,tr(s.cfg.Language,"node.create.yesno"));return true}
	 if want {
	  seed,err:=generateMLDSA65Seed()
	  if err!=nil{
	   log.Printf("[Bot] mldsa65 failed: %v",err)
	   s.sendAlert(chatID,tr(s.cfg.Language,"node.create.mldsafailed",err.Error()))
	   return true
	  }
	  state.Params.MLDSA65Seed=seed
	 }else{
	  state.Params.MLDSA65Seed=""
	 }
	case "cert_path":
	 if !filepath.IsAbs(input){s.sendAlert(chatID,tr(s.cfg.Language,"node.create.absolutepath"));return true}
	 state.Params.CertFile=filepath.Clean(input)
	case "key_path":
	 if !filepath.IsAbs(input){s.sendAlert(chatID,tr(s.cfg.Language,"node.create.absolutepath"));return true}
	 state.Params.KeyFile=filepath.Clean(input)
	default:
	 return false
	}
 nextNodeCreateStep(&state)
 if state.Step=="summary" {
  nodeCreateStates.Lock();delete(nodeCreateStates.items,chatID);nodeCreateStates.Unlock()
  s.showNodeCreateSummary(chatID,msg.From.ID,0,state)
  return true
 }
 if state.Step=="path"&&state.Params.Path=="" {
  path,err:=randomXHTTPPath();if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.create.failed",err.Error()));return true}
  state.Params.Path=path
 }
 nodeCreateStates.Lock();nodeCreateStates.items[chatID]=state;nodeCreateStates.Unlock()
 s.promptNodeCreate(chatID,0,state)
 return true
}

func (s *BotService) handleNodeCertSource(cb *tgbotapi.CallbackQuery) {
 if cb.Message==nil||cb.Message.Chat==nil||cb.From==nil{return}
 chatID:=cb.Message.Chat.ID
 nodeCreateStates.Lock()
 state,ok:=nodeCreateStates.items[chatID]
 if ok&&time.Now().After(state.Expires){delete(nodeCreateStates.items,chatID);ok=false}
 otherOwner:=ok&&state.AdminID!=cb.From.ID
 if ok&&(otherOwner||state.Step!="cert_source"){ok=false}
 nodeCreateStates.Unlock()
 if otherOwner{s.sendAlert(chatID,tr(s.cfg.Language,"manage.owned"));return}
 if !ok{s.sendOrEdit(chatID,cb.Message.MessageID,tr(s.cfg.Language,"node.create.expired"),s.backToMainMarkup());return}
 source:=strings.TrimPrefix(cb.Data,"node_cert_source:")
 if source!="self"&&source!="existing"{return}
 state.CertSource=source
 state.Expires=time.Now().Add(10*time.Minute)
 state.Step="path"
 if source=="existing"{state.Step="cert_path"}
 if state.Step=="path" {
  path,err:=randomXHTTPPath();if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.create.failed",err.Error()));return}
  state.Params.Path=path
 }
 nodeCreateStates.Lock();nodeCreateStates.items[chatID]=state;nodeCreateStates.Unlock()
 s.promptNodeCreate(chatID,cb.Message.MessageID,state)
}

func (s *BotService) sendNodeTemplatePicker(chatID int64,msgID int) {
 text:=tr(s.cfg.Language,"node.create.picker")
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.create.tcp"),"node_new:"+string(NodeTemplateTCPReality)),
  ),
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.create.xhttpreality"),"node_new:"+string(NodeTemplateXHTTPReality)),
  ),
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.create.xhttptls"),"node_new:"+string(NodeTemplateXHTTPTLS)),
  ),
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"inbound.native.button"),"ni:page:0"),
  ),
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.back"),"menu_nodes"),
  ),
 )
 s.sendOrEdit(chatID,msgID,text,markup)
}

func nodeCreateSummaryText(lang string,state nodeCreateState) string {
 encryption:=tr(lang,"node.encryption.disabled")
 if state.ClientEncryption!=""&&state.ClientEncryption!="none"{encryption=tr(lang,"node.encryption.enabled")}
 mldsa:=tr(lang,"node.encryption.disabled")
 if strings.TrimSpace(state.Params.MLDSA65Seed)!=""{mldsa=tr(lang,"node.encryption.enabled")}
 text:=tr(lang,"node.create.summary",
  mdEsc(nodeTemplateName(state.Template)),
  mdCode(state.Params.Tag),
  state.Params.Port,
  mdCode(state.Params.SNI),
 )
 if state.Template==NodeTemplateXHTTPReality||state.Template==NodeTemplateXHTTPTLS {
  text+=tr(lang,"node.create.summaryxhttp",mdCode(state.Params.Path),mdCode(state.Params.Mode))
 }
 if state.Template==NodeTemplateXHTTPTLS {
  text+=tr(lang,"node.create.summaryencryption",encryption)
 }else{
  text+=tr(lang,"node.create.summarysecrets",encryption,mldsa)
 }
 if state.Template==NodeTemplateXHTTPTLS {
  source:=tr(lang,"node.create.certself")
  if state.CertSource=="existing"{source=tr(lang,"node.create.certexisting")}
  text+=tr(lang,"node.create.summarycert",source,mdCode(state.Params.CertFile),mdCode(state.Params.KeyFile))
 }
 return text
}

func (s *BotService) showNodeCreateSummary(chatID,adminID int64,msgID int,state nodeCreateState) {
 state.Params=normalizedNodeParams(state.Params)
 request:=NodeCreateRequest{Template:state.Template,Params:state.Params,ClientEncryption:state.ClientEncryption}
 if state.Template==NodeTemplateTCPReality||state.Template==NodeTemplateXHTTPReality {
  if strings.TrimSpace(state.Params.PrivateKey)=="" {
   key,err:=resolveRealityPrivateKey(s.cfg.XrayConfigPath)
   if err!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.create.failed",err.Error()),s.backToMainMarkup());return}
   state.Params.PrivateKey=key
   request.Params=state.Params
  }
 }
 if state.Template==NodeTemplateXHTTPTLS {
  if state.CertSource=="self" {
   domain:=state.Params.SNI
   certPEM,keyPEM,err:=GenerateSelfSignedCertificatePEM(domain)
   if err!=nil{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.create.certfailed",err.Error()),s.backToMainMarkup());return}
   dir:=certsDirectory(s.cfg.XrayConfigPath)
   state.Params.CertFile=filepath.Join(dir,domain+".crt")
   state.Params.KeyFile=filepath.Join(dir,domain+".key")
   request.Params=state.Params
   request.CertPEM=certPEM
   request.KeyPEM=keyPEM
  }else{
   if err:=ValidateCertificatePair(state.Params.CertFile,state.Params.KeyFile,state.Params.SNI);err!=nil{
    s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.create.certfailed",err.Error()),s.backToMainMarkup());return
   }
  }
 }
 if err:=ValidateNodeTemplateParams(state.Template,state.Params);err!=nil{
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.create.failed",err.Error()),s.backToMainMarkup());return
 }
 var raw [8]byte
 if _,err:=rand.Read(raw[:]);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.tokenerror"));return}
 token:=hex.EncodeToString(raw[:])
 pendingNodeCreates.Lock()
 for key,item:=range pendingNodeCreates.items{if time.Now().After(item.Expires){delete(pendingNodeCreates.items,key)}}
 pendingNodeCreates.items[token]=pendingNodeCreate{ChatID:chatID,AdminID:adminID,Expires:time.Now().Add(3*time.Minute),Request:request}
 pendingNodeCreates.Unlock()
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.create.confirm"),"node_create_confirm:"+token),
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"),"node_create_cancel:"+token),
  ),
 )
 s.sendOrEdit(chatID,msgID,nodeCreateSummaryText(s.cfg.Language,state),markup)
}

func (s *BotService) confirmNodeCreate(cb *tgbotapi.CallbackQuery,confirm bool) {
 if cb.Message==nil||cb.Message.Chat==nil||cb.From==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 token:=strings.TrimPrefix(strings.TrimPrefix(cb.Data,"node_create_confirm:"),"node_create_cancel:")
 pendingNodeCreates.Lock()
 item,ok:=pendingNodeCreates.items[token]
 if ok&&item.ChatID==chatID&&item.AdminID==cb.From.ID{delete(pendingNodeCreates.items,token)}
 pendingNodeCreates.Unlock()
 if ok&&(item.ChatID!=chatID||item.AdminID!=cb.From.ID){s.sendAlert(chatID,tr(s.cfg.Language,"manage.owned"));return}
 if !ok||time.Now().After(item.Expires){
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.create.expired"),s.backToMainMarkup());return
 }
 if !confirm{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.create.cancelled"),s.backToMainMarkup());return}
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.mutationsdisabled"),s.backToMainMarkup());return}
 result,err:=CreateNode(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer,item.Request)
 if err!=nil{
  log.Printf("[Bot] node create failed: %v",err)
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.create.failed",err.Error()),s.backToMainMarkup());return
 }
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.create.success",result),s.backToMainMarkup())
}

func (s *BotService) showNodeDeleteConfirm(chatID,adminID int64,msgID int,key string) {
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.mutationsdisabled"),s.backToMainMarkup());return}
 nodes,err:=LoadXrayInbounds(s.cfg.XrayConfigPath)
 if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.readfailure",err.Error()));return}
 node:=findManagedNodeByKey(nodes,key)
 if node==nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.missing"));return}
 report,etag,err:=inspectNodeDeletionForConfirmation(s.cfg.XrayConfigPath,node.Tag)
 if err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.delete.inspectfailed",err.Error()));return}
 text:=tr(s.cfg.Language,"node.delete.impact",mdCode(node.Tag),report.Port,report.Clients)
 if len(report.RoutingRules)>0 {
  parts:=make([]string,0,len(report.RoutingRules))
  for _,index:=range report.RoutingRules{parts=append(parts,strconv.Itoa(index+1))}
  text+=tr(s.cfg.Language,"node.delete.routing",mdCode(strings.Join(parts,", ")))
 }
 if len(report.SuspendedUsers)>0 {
  text+=tr(s.cfg.Language,"node.delete.suspended",mdCode(strings.Join(report.SuspendedUsers,", ")))
 }
 if report.PublicPort>0{text+=tr(s.cfg.Language,"node.delete.publicport",report.PublicPort)}
 text+=tr(s.cfg.Language,"node.delete.note")
 var raw [8]byte
 if _,err:=rand.Read(raw[:]);err!=nil{s.sendAlert(chatID,tr(s.cfg.Language,"node.tokenerror"));return}
 token:=hex.EncodeToString(raw[:])
 pendingNodeDeletes.Lock()
 for old,item:=range pendingNodeDeletes.items{if time.Now().After(item.Expires){delete(pendingNodeDeletes.items,old)}}
 pendingNodeDeletes.items[token]=pendingNodeDelete{ChatID:chatID,AdminID:adminID,Expires:time.Now().Add(3*time.Minute),Tag:node.Tag,ETag:etag}
 pendingNodeDeletes.Unlock()
 markup:=tgbotapi.NewInlineKeyboardMarkup(
  tgbotapi.NewInlineKeyboardRow(
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"node.delete.confirm"),"node_delete_confirm:"+token),
   tgbotapi.NewInlineKeyboardButtonData(tr(s.cfg.Language,"common.cancel"),"node_delete_cancel:"+token),
  ),
 )
 s.sendOrEdit(chatID,msgID,text,markup)
}

func (s *BotService) confirmNodeDelete(cb *tgbotapi.CallbackQuery,confirm bool) {
 if cb.Message==nil||cb.Message.Chat==nil||cb.From==nil{return}
 chatID:=cb.Message.Chat.ID;msgID:=cb.Message.MessageID
 token:=strings.TrimPrefix(strings.TrimPrefix(cb.Data,"node_delete_confirm:"),"node_delete_cancel:")
 pendingNodeDeletes.Lock()
 item,ok:=pendingNodeDeletes.items[token]
 if ok&&item.ChatID==chatID&&item.AdminID==cb.From.ID{delete(pendingNodeDeletes.items,token)}
 pendingNodeDeletes.Unlock()
 if ok&&(item.ChatID!=chatID||item.AdminID!=cb.From.ID){s.sendAlert(chatID,tr(s.cfg.Language,"manage.owned"));return}
 if !ok||time.Now().After(item.Expires){s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.expired"),s.backToMainMarkup());return}
 if !confirm{s.sendNodeDetail(chatID,msgID,userCallbackKey(item.Tag));return}
 if !s.cfg.AllowMutations{s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.mutationsdisabled"),s.backToMainMarkup());return}
 result,err:=deleteNodeWithExpectedImpact(s.cfg.XrayConfigPath,s.cfg.XrayBinPath,s.cfg.StatsAPIServer,item.Tag,item.ETag)
 if err!=nil{
  if errors.Is(err,errNodeDeletionChanged){s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.delete.changed"),s.backToMainMarkup());return}
  log.Printf("[Bot] node delete failed: %v",err)
  s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.delete.failed",err.Error()),s.backToMainMarkup());return
 }
 s.sendOrEdit(chatID,msgID,tr(s.cfg.Language,"node.delete.success",result),s.backToMainMarkup())
}
