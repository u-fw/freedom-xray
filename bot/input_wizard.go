package main

import (
	"strings"
	"sync"
	"time"
)

type managementWizard struct {
	AdminID int64
	Expires time.Time
	Stage   string
	User    string
}

var managementWizards = struct {
	sync.Mutex
	states map[int64]managementWizard
}{states: make(map[int64]managementWizard)}

func (s *BotService) beginUserWizard(chatID, adminID int64, msgID int, stage, user string) {
	clearRoutingWizard(chatID)
	managementWizards.Lock()
	managementWizards.states[chatID] = managementWizard{AdminID: adminID, Expires: time.Now().Add(5 * time.Minute), Stage: stage, User: user}
	managementWizards.Unlock()
	prompt := tr(s.cfg.Language, "wizard.nameprompt")
	if stage == "rename" {
		prompt = tr(s.cfg.Language, "wizard.renameprompt", user)
	}
	s.sendOrEdit(chatID, msgID, prompt, s.backToMainMarkup())
}

func (s *BotService) handleWizardMessage(msg *tgMessage) bool {
	chatID := msg.Chat.ID
	managementWizards.Lock()
	state, ok := managementWizards.states[chatID]
	if ok && time.Now().After(state.Expires) {
		delete(managementWizards.states, chatID)
		ok = false
	}
	if ok && msg.From.ID != state.AdminID {
		ok = false
	}
	if ok && (msg.Command() == "" || msg.Command() == "cancel") {
		delete(managementWizards.states, chatID)
	}
	managementWizards.Unlock()
	if ok && msg.Command() == "cancel" {
		s.sendAlert(chatID, tr(s.cfg.Language, "wizard.cancelled"))
		return true
	}
	if !ok || msg.Command() != "" {
		return false
	}
	input := strings.TrimSpace(msg.Text)
	if !managedUserName.MatchString(input) {
		s.sendAlert(chatID, tr(s.cfg.Language, "wizard.invalidname"))
		return true
	}
	if state.Stage == "rename" {
		s.offerUserChange(chatID, msg.From.ID, 0, UserChange{Kind: "rename", User: state.User, NewName: input})
		return true
	}
	if state.Stage != "name" {
		return true
	}
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.inboundsfailed", err.Error()))
		return true
	}
	var rows [][]tgButton
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "wizard.createonly"), "manage_scope:unbound")))
	rows = append(rows, newRow(newButton(tr(s.cfg.Language, "wizard.assignall"), "manage_scope:all")))
	for _, n := range nodes {
		if n.Protocol != "vless" || n.Tag == "" {
			continue
		}
		rows = append(rows, newRow(
			newButton(truncateLabel(n.Tag, 48), "manage_scope:"+userCallbackKey(n.Tag)),
		))
	}
	managementWizards.Lock()
	managementWizards.states[chatID] = managementWizard{AdminID: msg.From.ID, Expires: time.Now().Add(5 * time.Minute), Stage: "scope", User: input}
	managementWizards.Unlock()
	s.sendOrEdit(chatID, 0, tr(s.cfg.Language, "wizard.scopebody", input), newKeyboard(rows...))
	return true
}

func (s *BotService) handleWizardScope(cb *tgCallback) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	managementWizards.Lock()
	state, ok := managementWizards.states[chatID]
	if ok && state.AdminID == cb.From.ID && state.Stage == "scope" && time.Now().Before(state.Expires) {
		delete(managementWizards.states, chatID)
	} else {
		ok = false
	}
	managementWizards.Unlock()
	if !ok {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "wizard.expired"), s.backToMainMarkup())
		return
	}
	key := strings.TrimPrefix(cb.Data, "manage_scope:")
	target := ""
	if key == "all" {
		target = "all"
	}
	if key != "all" && key != "unbound" {
		nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendAlert(chatID, tr(s.cfg.Language, "wizard.inboundread"))
			return
		}
		target = ""
		for _, n := range nodes {
			if n.Protocol == "vless" && n.Tag != "" && userCallbackKey(n.Tag) == key {
				target = n.Tag
				break
			}
		}
		if target == "" {
			s.sendAlert(chatID, tr(s.cfg.Language, "wizard.inboundchanged"))
			return
		}
	}
	s.offerUserChange(chatID, cb.From.ID, msgID, UserChange{Kind: "add", User: state.User, Node: target})
}
