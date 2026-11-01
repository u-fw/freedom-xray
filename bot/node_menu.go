package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type pendingNodeChange struct {
	Change  NodeChange
	ChatID  int64
	AdminID int64
	Expires time.Time
}
type nodePathWizard struct {
	Tag     string
	AdminID int64
	Expires time.Time
	Kind    string
	Value   string
}

var pendingNodes = struct {
	sync.Mutex
	items map[string]pendingNodeChange
}{items: map[string]pendingNodeChange{}}
var nodePathWizards = struct {
	sync.Mutex
	items map[int64]nodePathWizard
}{items: map[int64]nodePathWizard{}}

func findManagedNodeByKey(nodes []InboundDetails, key string) *InboundDetails {
	var found *InboundDetails
	for i := range nodes {
		if nodes[i].Protocol != "vless" || userCallbackKey(nodes[i].Tag) != key {
			continue
		}
		if found != nil {
			return nil
		} // Reject ambiguous tags/callbacks.
		found = &nodes[i]
	}
	return found
}

func maskRealityShortID(shortID, lang string) string {
	if shortID == "" {
		return tr(lang, "node.notconfigured")
	}
	if len(shortID) <= 4 {
		return tr(lang, "node.hidden")
	}
	return shortID[:4] + "…"
}

func nodeChangeFieldLabel(lang, kind string) string {
	switch kind {
	case "reality_sni":
		return tr(lang, "node.field.sni")
	case "reality_short_id":
		return tr(lang, "node.field.shortid")
	case "path":
		return tr(lang, "node.field.path")
	case "mode":
		return tr(lang, "node.field.mode")
	case "public_port":
		return tr(lang, "node.field.publicport")
	case "port":
		return tr(lang, "node.field.listenport")
	case "listen":
		return tr(lang, "node.field.listenaddress")
	case "xhttp_host":
		return tr(lang, "node.field.host")
	case "xhttp_padding":
		return tr(lang, "node.field.padding")
	case "reality_dest":
		return tr(lang, "node.field.dest")
	case "reality_target":
		return tr(lang, "node.field.target")
	case "reality_private_key":
		return tr(lang, "node.field.privatekey")
	case "reality_mldsa_seed":
		return tr(lang, "node.field.mldsa")
	case "decryption":
		return tr(lang, "node.field.decryption")
	case "tls_pair":
		return tr(lang, "node.field.certpair")
	case "alpn":
		return tr(lang, "node.field.alpn")
	}
	return kind
}

func (s *BotService) startNodeWizard(chatID, adminID int64, msgID int, tag, kind string, prompt htmlText) {
	s.startNodeWizardValue(chatID, adminID, msgID, tag, kind, prompt, "")
}

func (s *BotService) startNodeWizardValue(chatID, adminID int64, msgID int, tag, kind string, prompt htmlText, value string) {
	clearInputSessions(chatID)
	nodePathWizards.Lock()
	nodePathWizards.items[chatID] = nodePathWizard{Tag: tag, AdminID: adminID, Expires: time.Now().Add(5 * time.Minute), Kind: kind, Value: value}
	nodePathWizards.Unlock()
	s.sendOrEdit(chatID, msgID, prompt, s.backToMainMarkup())
}

func (s *BotService) sendNodeDetail(chatID int64, msgID int, key string) {
	s.sendNodeDetailSection(chatID, msgID, key, false)
}

func (s *BotService) sendNodeDetailSection(chatID int64, msgID int, key string, advanced bool) {
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.readfailure", err.Error()), s.backToMainMarkup())
		return
	}
	node := findManagedNodeByKey(nodes, key)
	if node == nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.notfound"), s.backToMainMarkup())
		return
	}

	msg := trh(s.cfg.Language, "node.detail",
		node.Tag, strings.ToUpper(node.Protocol), strings.ToUpper(node.Network), strings.ToUpper(node.Security),
		node.Port, node.SharePort(), len(node.Clients))
	rows := [][]tgButton{}
	encryptionState := tr(s.cfg.Language, "node.encryption.disabled")
	if node.Decryption != "" && node.Decryption != "none" {
		encryptionState = tr(s.cfg.Language, "node.encryption.enabled")
	}
	mldsaState := tr(s.cfg.Language, "node.encryption.disabled")
	if node.MLDSA65Seed != "" {
		mldsaState = tr(s.cfg.Language, "node.encryption.enabled")
	}

	if node.Security == "reality" {
		msg += trh(s.cfg.Language, "node.realitydetail", node.SNI, maskRealityShortID(node.ShortID, s.cfg.Language))
		dest := node.Dest
		if dest == "" {
			dest = tr(s.cfg.Language, "node.notconfigured")
		}
		target := node.Target
		if target == "" {
			target = tr(s.cfg.Language, "node.notconfigured")
		}
		msg += trh(s.cfg.Language, "node.realityadvanced", dest, target, encryptionState, mldsaState)
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "node.edit.sni"), "node_sni:"+key),
			newButton(tr(s.cfg.Language, "node.edit.shortid"), "node_short_id:"+key),
		))
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "node.edit.dest"), "node_dest:"+key),
			newButton(tr(s.cfg.Language, "node.edit.target"), "node_target:"+key),
		))
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "node.edit.privatekey"), "node_reality_key:"+key)))
		mldsaLabel := tr(s.cfg.Language, "node.edit.mldsaenable")
		mldsaAction := "set"
		if node.MLDSA65Seed != "" {
			mldsaLabel = tr(s.cfg.Language, "node.edit.mldsadisable")
			mldsaAction = "clear"
		}
		rows = append(rows, newRow(newButton(mldsaLabel, "node_mldsa:"+key+":"+mldsaAction)))
	}
	if node.Network == "xhttp" {
		mode := node.XHTTPMode
		if mode == "" {
			mode = "auto"
		}
		host := node.XHTTPHost
		if host == "" {
			host = tr(s.cfg.Language, "node.notconfigured")
		}
		padding := node.XHTTPPaddingBytes
		if padding == "" {
			padding = tr(s.cfg.Language, "node.default")
		}
		msg += trh(s.cfg.Language, "node.xhttpdetail", node.XHTTPPath, mode, host, padding)
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "node.edit.path"), "node_path:"+key),
			newButton(tr(s.cfg.Language, "node.edit.host"), "node_xhttp_host:"+key),
		))
		rows = append(rows, newRow(newButton(tr(s.cfg.Language, "node.edit.padding"), "node_xhttp_padding:"+key)))
		if node.Security == "tls" {
			msg += trh(s.cfg.Language, "node.publicnote")
			rows = append(rows, newRow(newButton(tr(s.cfg.Language, "node.edit.publicport"), "node_public_port:"+key)))
		}
		modes := []string{"auto", "packet-up", "stream-up", "stream-one"}
		for i := 0; i < len(modes); i += 2 {
			buttons := make([]tgButton, 0, 2)
			for _, option := range modes[i : i+2] {
				label := option
				if option == mode {
					label = "● " + option
				}
				buttons = append(buttons, newButton(label, "node_mode:"+key+":"+option))
			}
			rows = append(rows, newRow(buttons...))
		}
	}
	if node.Security == "tls" {
		cert := node.CertFile
		if cert == "" {
			cert = tr(s.cfg.Language, "node.notconfigured")
		}
		certKey := node.KeyFile
		if certKey == "" {
			certKey = tr(s.cfg.Language, "node.notconfigured")
		}
		alpn := tr(s.cfg.Language, "node.default")
		if len(node.ALPN) > 0 {
			alpn = strings.Join(node.ALPN, ", ")
		}
		msg += trh(s.cfg.Language, "node.tlsdetail", cert, certKey, alpn)
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "node.edit.certpair"), "node_cert_pair:"+key),
			newButton(tr(s.cfg.Language, "node.edit.alpn"), "node_alpn:"+key),
		))
	}
	if node.Security == "reality" || (node.Network == "xhttp" && node.Security == "tls") {
		action := "enable"
		label := tr(s.cfg.Language, "node.edit.encryptionenable")
		if encryptionState == tr(s.cfg.Language, "node.encryption.enabled") {
			action = "disable"
			label = tr(s.cfg.Language, "node.edit.encryptiondisable")
		}
		rows = append(rows, newRow(
			newButton(label+" ("+encryptionState+")", "node_decryption:"+key+":"+action),
		))
	}
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "node.edit.listenport"), "node_listen_port:"+key),
		newButton(tr(s.cfg.Language, "node.edit.listenaddress"), "node_listen_address:"+key),
	))
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "node.delete.button"), "node_delete:"+key)))
	var visible [][]tgButton
	if s.cfg.AllowMutations {
		for _, row := range rows {
			var selected []tgButton
			for _, button := range row {
				if button.Data == "" {
					continue
				}
				action := strings.SplitN(button.Data, ":", 2)[0]
				basic := action == "node_sni" || action == "node_short_id" || action == "node_path" || action == "node_xhttp_host" || action == "node_mode" || action == "node_public_port" || action == "node_listen_port"
				if basic != advanced {
					selected = append(selected, button)
				}
			}
			if len(selected) > 0 {
				visible = append(visible, selected)
			}
		}
		label, action := tr(s.cfg.Language, "node.more"), "node_advanced:"
		if advanced {
			label, action = tr(s.cfg.Language, "node.basic"), "node_basic:"
		}
		visible = append(visible, newRow(newButton(label, action+key)))
	}
	offset := 0
	for i := range nodes {
		if &nodes[i] == node {
			offset = (i / nodesPerPage) * nodesPerPage
			break
		}
	}
	visible = append(visible, newRow(
		newButton(tr(s.cfg.Language, "node.backlist"), fmt.Sprintf("nodes_page:%d", offset)),
		newButton(tr(s.cfg.Language, "menu.home"), "menu_main")))
	s.sendOrEdit(chatID, msgID, msg, newKeyboard(visible...))
}

func (s *BotService) offerNodeChange(chatID, adminID int64, msgID int, change NodeChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.mutationsdisabled"), s.backToMainMarkup())
		return
	}
	var randKey [8]byte
	if _, err := rand.Read(randKey[:]); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "node.tokenerror"))
		return
	}
	key := hex.EncodeToString(randKey[:])
	pendingNodes.Lock()
	for token, v := range pendingNodes.items {
		if time.Now().After(v.Expires) {
			delete(pendingNodes.items, token)
		}
	}
	pendingNodes.items[key] = pendingNodeChange{Change: change, ChatID: chatID, AdminID: adminID, Expires: time.Now().Add(3 * time.Minute)}
	pendingNodes.Unlock()
	note := trh(s.cfg.Language, "node.changenote.generic")
	switch change.Kind {
	case "public_port":
		note = trh(s.cfg.Language, "node.changenote.publicport")
	case "reality_sni":
		note = trh(s.cfg.Language, "node.changenote.sni")
	case "port":
		note = trh(s.cfg.Language, "node.changenote.port")
	case "xhttp_host":
		note = trh(s.cfg.Language, "node.changenote.host")
	case "xhttp_padding":
		note = trh(s.cfg.Language, "node.changenote.padding")
	case "reality_short_id":
		note = trh(s.cfg.Language, "node.changenote.shortid")
	case "reality_dest":
		note = trh(s.cfg.Language, "node.changenote.dest")
	case "reality_target":
		note = trh(s.cfg.Language, "node.changenote.target")
	case "tls_pair":
		note = trh(s.cfg.Language, "node.changenote.certpair")
	case "alpn":
		note = trh(s.cfg.Language, "node.changenote.alpn")
	}
	value := change.Value
	if value == "" {
		value = tr(s.cfg.Language, "node.clear")
	}
	body := trh(s.cfg.Language, "node.changebody", change.Tag, nodeChangeFieldLabel(s.cfg.Language, change.Kind), value, note)
	keyboard := newKeyboard(newRow(
		newButton(tr(s.cfg.Language, "common.confirm"), "node_confirm:"+key),
		newButton(tr(s.cfg.Language, "common.cancel"), "node_cancel:"+key)))
	s.sendOrEdit(chatID, msgID, body, keyboard)
}

// offerNodeSecretChange confirms an operation whose new value is generated by
// the Bot or intentionally omitted. Secret material is never rendered.
func (s *BotService) offerNodeSecretChange(chatID, adminID int64, msgID int, change NodeChange, noteKey string) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.mutationsdisabled"), s.backToMainMarkup())
		return
	}
	var randKey [8]byte
	if _, err := rand.Read(randKey[:]); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "node.tokenerror"))
		return
	}
	key := hex.EncodeToString(randKey[:])
	pendingNodes.Lock()
	for token, v := range pendingNodes.items {
		if time.Now().After(v.Expires) {
			delete(pendingNodes.items, token)
		}
	}
	pendingNodes.items[key] = pendingNodeChange{Change: change, ChatID: chatID, AdminID: adminID, Expires: time.Now().Add(3 * time.Minute)}
	pendingNodes.Unlock()
	body := trh(s.cfg.Language, "node.secretchange", change.Tag, nodeChangeFieldLabel(s.cfg.Language, change.Kind), trh(s.cfg.Language, noteKey))
	keyboard := newKeyboard(newRow(
		newButton(tr(s.cfg.Language, "common.confirm"), "node_confirm:"+key),
		newButton(tr(s.cfg.Language, "common.cancel"), "node_cancel:"+key)))
	s.sendOrEdit(chatID, msgID, body, keyboard)
}

// offerNodeTLSPair shows both paths because a certificate replacement must be
// committed as one pair; validating either half alone would always fail.
func (s *BotService) offerNodeTLSPair(chatID, adminID int64, msgID int, change NodeChange, noteKey string) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.mutationsdisabled"), s.backToMainMarkup())
		return
	}
	var randKey [8]byte
	if _, err := rand.Read(randKey[:]); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "node.tokenerror"))
		return
	}
	key := hex.EncodeToString(randKey[:])
	pendingNodes.Lock()
	for token, v := range pendingNodes.items {
		if time.Now().After(v.Expires) {
			delete(pendingNodes.items, token)
		}
	}
	pendingNodes.items[key] = pendingNodeChange{Change: change, ChatID: chatID, AdminID: adminID, Expires: time.Now().Add(3 * time.Minute)}
	pendingNodes.Unlock()
	body := trh(s.cfg.Language, "node.certpairchange", change.Tag, change.Value, change.Secret, trh(s.cfg.Language, noteKey))
	keyboard := newKeyboard(newRow(
		newButton(tr(s.cfg.Language, "common.confirm"), "node_confirm:"+key),
		newButton(tr(s.cfg.Language, "common.cancel"), "node_cancel:"+key)))
	s.sendOrEdit(chatID, msgID, body, keyboard)
}

func (s *BotService) confirmNodeChange(cb *tgCallback, confirm bool) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	token := strings.TrimPrefix(strings.TrimPrefix(cb.Data, "node_confirm:"), "node_cancel:")
	pendingNodes.Lock()
	p, ok := pendingNodes.items[token]
	if ok && p.ChatID == chatID && p.AdminID == cb.From.ID {
		delete(pendingNodes.items, token)
	}
	pendingNodes.Unlock()
	if ok && (p.ChatID != chatID || p.AdminID != cb.From.ID) {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || time.Now().After(p.Expires) {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.expired"), s.backToMainMarkup())
		return
	}
	if !confirm {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.cancelled"), s.backToMainMarkup())
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.denied"), s.backToMainMarkup())
		return
	}
	var result string
	var err error
	if p.Change.Kind == "public_port" {
		port, parseErr := strconv.Atoi(p.Change.Value)
		if parseErr != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.invalidport"))
			return
		}
		result, err = SetInboundPublicPort(s.cfg.XrayConfigPath, p.Change.Tag, port)
	} else {
		result, err = ChangeNodeConfig(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, p.Change)
	}
	if err != nil {
		log.Printf("[Bot] node update failed: %v", err)
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.updatefailed", err.Error()), s.backToMainMarkup())
		return
	}
	s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "node.updatesuccess", result), s.backToMainMarkup())
}

func (s *BotService) handleNodeCallback(cb *tgCallback) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	parts := strings.Split(cb.Data, ":")
	if len(parts) < 2 {
		return
	}
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "node.readfailure", err.Error()))
		return
	}
	node := findManagedNodeByKey(nodes, parts[1])
	if node == nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "node.missing"))
		return
	}
	switch parts[0] {
	case "node_detail":
		s.sendNodeDetail(chatID, msgID, parts[1])
	case "node_advanced":
		s.sendNodeDetailSection(chatID, msgID, parts[1], true)
	case "node_basic":
		s.sendNodeDetailSection(chatID, msgID, parts[1], false)
	case "node_sni":
		if node.Security != "reality" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.notreality"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "reality_sni", trh(s.cfg.Language, "node.sniprompt"))
	case "node_short_id":
		if node.Security != "reality" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.notreality"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "reality_short_id", trh(s.cfg.Language, "node.shortidprompt", maskRealityShortID(node.ShortID, s.cfg.Language)))
	case "node_path":
		if node.Network != "xhttp" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.notxhttp"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "path", trh(s.cfg.Language, "node.pathprompt"))
	case "node_xhttp_host":
		if node.Network != "xhttp" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.notxhttp"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		current := node.XHTTPHost
		if current == "" {
			current = tr(s.cfg.Language, "node.notconfigured")
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "xhttp_host", trh(s.cfg.Language, "node.hostprompt", current))
	case "node_xhttp_padding":
		if node.Network != "xhttp" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.notxhttp"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		current := node.XHTTPPaddingBytes
		if current == "" {
			current = tr(s.cfg.Language, "node.default")
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "xhttp_padding", trh(s.cfg.Language, "node.paddingprompt", current))
	case "node_public_port":
		if node.Network != "xhttp" || node.Security != "tls" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.portonly"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.mutationsdisabled"))
			return
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "public_port", trh(s.cfg.Language, "node.portprompt", node.Port))
	case "node_listen_port":
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "port", trh(s.cfg.Language, "node.listenportprompt", node.Port))
	case "node_listen_address":
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "listen", trh(s.cfg.Language, "node.listenaddressprompt"))
	case "node_mode":
		if node.Network != "xhttp" || len(parts) != 3 {
			return
		}
		s.offerNodeChange(chatID, cb.From.ID, msgID, NodeChange{Kind: "mode", Tag: node.Tag, Value: parts[2]})
	case "node_dest", "node_target":
		if node.Security != "reality" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.notreality"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		kind := "reality_dest"
		current := node.Dest
		promptKey := "node.destprompt"
		if parts[0] == "node_target" {
			kind = "reality_target"
			current = node.Target
			promptKey = "node.targetprompt"
		}
		if current == "" {
			current = node.SNI + ":443"
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, kind, trh(s.cfg.Language, promptKey, current))
	case "node_reality_key":
		if node.Security != "reality" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.notreality"))
			return
		}
		private, _, err := generateRealityKeyPair()
		if err != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.secretgeneratefailed", err.Error()))
			return
		}
		s.offerNodeSecretChange(chatID, cb.From.ID, msgID, NodeChange{Kind: "reality_private_key", Tag: node.Tag, Secret: private}, "node.secretnote.privatekey")
	case "node_mldsa":
		if node.Security != "reality" || len(parts) != 3 {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.notreality"))
			return
		}
		if parts[2] == "clear" {
			s.offerNodeSecretChange(chatID, cb.From.ID, msgID, NodeChange{Kind: "reality_mldsa_seed", Tag: node.Tag}, "node.secretnote.mldsaclear")
			return
		}
		seed, err := generateMLDSA65Seed()
		if err != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.secretgeneratefailed", err.Error()))
			return
		}
		s.offerNodeSecretChange(chatID, cb.From.ID, msgID, NodeChange{Kind: "reality_mldsa_seed", Tag: node.Tag, Secret: seed}, "node.secretnote.mldsaset")
	case "node_decryption":
		if len(parts) != 3 {
			return
		}
		if parts[2] == "disable" {
			s.offerNodeSecretChange(chatID, cb.From.ID, msgID, NodeChange{Kind: "decryption", Tag: node.Tag, Secret: "none", ClientSecret: "none"}, "node.secretnote.encryptiondisable")
			return
		}
		dec, enc, err := generateVLESSEncryptionPair()
		if err != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.secretgeneratefailed", err.Error()))
			return
		}
		s.offerNodeSecretChange(chatID, cb.From.ID, msgID, NodeChange{Kind: "decryption", Tag: node.Tag, Secret: dec, ClientSecret: enc}, "node.secretnote.encryptionenable")
	case "node_cert_pair":
		if node.Security != "tls" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.nottls"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		current := node.CertFile
		if current == "" {
			current = tr(s.cfg.Language, "node.notconfigured")
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "tls_pair_cert", trh(s.cfg.Language, "node.certpairprompt", current))
	case "node_alpn":
		if node.Security != "tls" {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.nottls"))
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "node.enablemutations"))
			return
		}
		current := strings.Join(node.ALPN, ", ")
		if current == "" {
			current = "h2, http/1.1"
		}
		s.startNodeWizard(chatID, cb.From.ID, msgID, node.Tag, "alpn", trh(s.cfg.Language, "node.alpnprompt", current))
	}
}

func (s *BotService) handleNodePathInput(msg *tgMessage) bool {
	chatID := msg.Chat.ID
	nodePathWizards.Lock()
	state, ok := nodePathWizards.items[chatID]
	if !ok {
		nodePathWizards.Unlock()
		return false
	}
	if time.Now().After(state.Expires) {
		delete(nodePathWizards.items, chatID)
		nodePathWizards.Unlock()
		return false
	}
	if state.AdminID != msg.From.ID {
		nodePathWizards.Unlock()
		return false
	}
	if msg.Command() == "cancel" {
		delete(nodePathWizards.items, chatID)
		nodePathWizards.Unlock()
		s.retryInput(chatID, trh(s.cfg.Language, "node.cancelinput"))
		return true
	}
	if msg.Command() != "" {
		nodePathWizards.Unlock()
		return false
	}
	nodePathWizards.Unlock()
	value := strings.TrimSpace(msg.Text)
	switch state.Kind {
	case "public_port":
		port, err := strconv.Atoi(value)
		if err != nil || port < 0 || port > 65535 {
			s.retryInput(chatID, trh(s.cfg.Language, "node.portinvalid"))
			return true
		}
	case "port":
		if _, err := validateListenPort(value); err != nil {
			s.retryInput(chatID, trh(s.cfg.Language, "node.listenportinvalid"))
			return true
		}
	case "listen":
		if value == "-" {
			value = ""
		}
		if value != "" && net.ParseIP(value) == nil {
			s.retryInput(chatID, trh(s.cfg.Language, "node.listenaddressinvalid"))
			return true
		}
	case "path":
		if !safeXHTTPPath.MatchString(value) {
			s.retryInput(chatID, trh(s.cfg.Language, "node.pathinvalid"))
			return true
		}
	case "reality_sni":
		if err := validateRealitySNI(value); err != nil {
			s.retryInput(chatID, trh(s.cfg.Language, "node.sniinvalid", err.Error()))
			return true
		}
	case "reality_short_id":
		if value == "-" {
			value = ""
		}
		if err := validateRealityShortID(value); err != nil {
			s.retryInput(chatID, trh(s.cfg.Language, "node.shortidinvalid", err.Error()))
			return true
		}
	case "xhttp_host":
		if value == "-" {
			value = ""
		}
		if err := validateXHTTPHost(value); err != nil {
			s.retryInput(chatID, trh(s.cfg.Language, "node.hostinvalid", err.Error()))
			return true
		}
	case "xhttp_padding":
		if err := validateXHTTPPadding(value); err != nil {
			s.retryInput(chatID, trh(s.cfg.Language, "node.paddinginvalid", err.Error()))
			return true
		}
	case "reality_dest", "reality_target":
		if _, err := validateRealityTarget(value); err != nil {
			s.retryInput(chatID, trh(s.cfg.Language, "node.targetinvalid", err.Error()))
			return true
		}
	case "tls_pair_cert":
		if !filepath.IsAbs(value) {
			s.retryInput(chatID, trh(s.cfg.Language, "node.create.absolutepath"))
			return true
		}
		certPath := filepath.Clean(value)
		s.startNodeWizardValue(chatID, msg.From.ID, 0, state.Tag, "tls_pair_key", trh(s.cfg.Language, "node.keypairprompt", certPath), certPath)
		return true
	case "tls_pair_key":
		if !filepath.IsAbs(value) {
			s.retryInput(chatID, trh(s.cfg.Language, "node.create.absolutepath"))
			return true
		}
		endNodePathInput(chatID)
		s.offerNodeTLSPair(chatID, msg.From.ID, 0, NodeChange{Kind: "tls_pair", Tag: state.Tag, Value: state.Value, Secret: filepath.Clean(value)}, "node.secretnote.certpair")
		return true
	case "alpn":
		if _, err := validateNodeALPNText(value); err != nil {
			s.retryInput(chatID, trh(s.cfg.Language, "node.alpninvalid", err.Error()))
			return true
		}
	default:
		endNodePathInput(chatID)
		s.sendAlert(chatID, trh(s.cfg.Language, "node.unknownop"))
		return true
	}
	endNodePathInput(chatID)
	s.offerNodeChange(chatID, msg.From.ID, 0, NodeChange{Kind: state.Kind, Tag: state.Tag, Value: value})
	return true
}

func endNodePathInput(chatID int64) {
	nodePathWizards.Lock()
	delete(nodePathWizards.items, chatID)
	nodePathWizards.Unlock()
}
