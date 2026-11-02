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
	// Picked holds the inbounds chosen for a new user on the scope screen.
	Picked []string
}

var managementWizards = struct {
	sync.Mutex
	states map[int64]managementWizard
}{states: make(map[int64]managementWizard)}

func (s *BotService) beginUserWizard(chatID, adminID int64, msgID int, stage, user string) {
	clearInputSessions(chatID)
	managementWizards.Lock()
	managementWizards.states[chatID] = managementWizard{AdminID: adminID, Expires: time.Now().Add(5 * time.Minute), Stage: stage, User: user}
	managementWizards.Unlock()
	prompt := trh(s.cfg.Language, "wizard.nameprompt")
	if stage == "rename" {
		prompt = trh(s.cfg.Language, "wizard.renameprompt", user)
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
	if ok && msg.Command() == "cancel" {
		delete(managementWizards.states, chatID)
	}
	managementWizards.Unlock()
	if ok && msg.Command() == "cancel" {
		s.sendAlert(chatID, trh(s.cfg.Language, "wizard.cancelled"))
		return true
	}
	if !ok || msg.Command() != "" {
		return false
	}
	input := strings.TrimSpace(msg.Text)
	if !managedUserName.MatchString(input) {
		s.retryInput(chatID, trh(s.cfg.Language, "wizard.invalidname"))
		return true
	}
	managementWizards.Lock()
	delete(managementWizards.states, chatID)
	managementWizards.Unlock()
	if state.Stage == "rename" {
		s.offerUserChange(chatID, msg.From.ID, 0, UserChange{Kind: "rename", User: state.User, NewName: input})
		return true
	}
	if state.Stage != "name" {
		return true
	}
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.inboundsfailed", err.Error()))
		return true
	}
	state = managementWizard{AdminID: msg.From.ID, Expires: time.Now().Add(5 * time.Minute), Stage: "scope", User: input}
	managementWizards.Lock()
	managementWizards.states[chatID] = state
	managementWizards.Unlock()
	s.sendUserScope(chatID, 0, state, nodes)
	return true
}

// sendUserScope lets the admin choose the inbounds a new user may use.
func (s *BotService) sendUserScope(chatID int64, msgID int, state managementWizard, nodes []InboundDetails) {
	lang := s.cfg.Language
	picked := map[string]bool{}
	for _, tag := range state.Picked {
		picked[tag] = true
	}
	var rows [][]tgButton
	for _, n := range nodes {
		if n.Protocol != "vless" || n.Tag == "" {
			continue
		}
		mark := "☐ "
		if picked[n.Tag] {
			mark = "☑ "
		}
		rows = append(rows, newRow(newButton(mark+truncateLabel(n.Tag, 44), "manage_pick:"+userCallbackKey(n.Tag))))
	}
	if len(state.Picked) > 0 {
		rows = append(rows, newRow(newButton(tr(lang, "wizard.pickcontinue", len(state.Picked)), "manage_scope:picked")))
	}
	rows = append(rows, newRow(newButton(tr(lang, "wizard.createonly"), "manage_scope:unbound")))
	rows = append(rows, newRow(newButton(tr(lang, "wizard.assignall"), "manage_scope:all")))
	s.sendOrEdit(chatID, msgID, trh(lang, "wizard.scopebody", state.User), newKeyboard(rows...))
}

// handleWizardPick toggles one inbound for the user being created.
func (s *BotService) handleWizardPick(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID, msgID := cb.Message.Chat.ID, cb.Message.MessageID
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "wizard.inboundread"))
		return
	}
	key := strings.TrimPrefix(cb.Data, "manage_pick:")
	tag := ""
	for _, n := range nodes {
		if n.Protocol == "vless" && n.Tag != "" && userCallbackKey(n.Tag) == key {
			tag = n.Tag
			break
		}
	}
	managementWizards.Lock()
	state, ok := managementWizards.states[chatID]
	valid := ok && state.AdminID == cb.From.ID && state.Stage == "scope" && time.Now().Before(state.Expires)
	if valid && tag != "" {
		picked := make([]string, 0, len(state.Picked)+1)
		removed := false
		for _, existing := range state.Picked {
			if existing == tag {
				removed = true
				continue
			}
			picked = append(picked, existing)
		}
		if !removed {
			picked = append(picked, tag)
		}
		state.Picked = picked
		state.Expires = time.Now().Add(5 * time.Minute)
		managementWizards.states[chatID] = state
	}
	managementWizards.Unlock()
	if !valid {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "wizard.expired"), s.backToMainMarkup())
		return
	}
	if tag == "" {
		s.sendAlert(chatID, trh(s.cfg.Language, "wizard.inboundchanged"))
		return
	}
	s.sendUserScope(chatID, msgID, state, nodes)
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
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "wizard.expired"), s.backToMainMarkup())
		return
	}
	key := strings.TrimPrefix(cb.Data, "manage_scope:")
	if key == "picked" {
		if len(state.Picked) == 0 {
			s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "wizard.expired"), s.backToMainMarkup())
			return
		}
		s.offerUserChange(chatID, cb.From.ID, msgID, UserChange{Kind: "add", User: state.User, Nodes: state.Picked})
		return
	}
	target := ""
	if key == "all" {
		target = "all"
	}
	if key != "all" && key != "unbound" {
		nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
		if err != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "wizard.inboundread"))
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
			s.sendAlert(chatID, trh(s.cfg.Language, "wizard.inboundchanged"))
			return
		}
	}
	s.offerUserChange(chatID, cb.From.ID, msgID, UserChange{Kind: "add", User: state.User, Node: target})
}
