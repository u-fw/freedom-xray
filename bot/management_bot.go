package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

type pendingUserChange struct {
	Change    UserChange
	ChatID    int64
	AdminID   int64
	ExpiresAt time.Time
}

var pendingUserChanges = struct {
	sync.Mutex
	items map[string]pendingUserChange
}{items: make(map[string]pendingUserChange)}

func (s *BotService) offerUserChange(chatID, adminID int64, messageID int, change UserChange) {
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, messageID, trh(s.cfg.Language, "manage.disabled"), s.backToMainMarkup())
		return
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.tokenerror", err.Error()))
		return
	}
	key := hex.EncodeToString(token[:])
	pendingUserChanges.Lock()
	now := time.Now()
	for k, v := range pendingUserChanges.items {
		if now.After(v.ExpiresAt) {
			delete(pendingUserChanges.items, k)
		}
	}
	pendingUserChanges.items[key] = pendingUserChange{Change: change, ChatID: chatID, AdminID: adminID, ExpiresAt: now.Add(3 * time.Minute)}
	pendingUserChanges.Unlock()

	var description string
	switch change.Kind {
	case "add":
		if len(change.Nodes) > 0 {
			description = tr(s.cfg.Language, "manage.add.node", change.User, strings.Join(change.Nodes, ", "))
		} else if change.Node == "" {
			description = tr(s.cfg.Language, "manage.add.unbound", change.User)
		} else {
			description = tr(s.cfg.Language, "manage.add.node", change.User, scopeLabel(change.Node, s.cfg.Language))
		}
	case "rename":
		description = tr(s.cfg.Language, "manage.rename", change.User, change.NewName)
	case "rotate":
		description = tr(s.cfg.Language, "manage.rotate", change.User)
	case "delete":
		description = tr(s.cfg.Language, "manage.delete", change.User)
	case "assign":
		description = tr(s.cfg.Language, "manage.assign", change.User, change.Node)
	case "unassign":
		description = tr(s.cfg.Language, "manage.unassign", change.User, change.Node)
	case "disable":
		description = tr(s.cfg.Language, "manage.disable", change.User)
	case "enable":
		description = tr(s.cfg.Language, "manage.enable", change.User)
	default:
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.unknown"))
		return
	}
	markup := newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "manage.confirm"), "manage_confirm:"+key),
			newButton(tr(s.cfg.Language, "common.cancel"), "manage_cancel:"+key),
		),
	)
	s.sendOrEdit(chatID, messageID, trh(s.cfg.Language, "manage.confirmbody", description), markup)
}

func scopeLabel(node, lang string) string {
	if node == "" || node == "all" {
		return tr(lang, "manage.scope.all")
	}
	return node
}

func (s *BotService) resolveUserChange(cb *tgCallback, confirm bool) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	key := strings.TrimPrefix(strings.TrimPrefix(cb.Data, "manage_confirm:"), "manage_cancel:")
	pendingUserChanges.Lock()
	request, ok := pendingUserChanges.items[key]
	if ok && request.ChatID == chatID && request.AdminID == cb.From.ID {
		delete(pendingUserChanges.items, key)
	}
	pendingUserChanges.Unlock()
	if ok && (request.ChatID != chatID || request.AdminID != cb.From.ID) {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || time.Now().After(request.ExpiresAt) {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.expired"), s.backToMainMarkup())
		return
	}
	if !confirm {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.cancelled"), s.backToMainMarkup())
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	msg, err := changeManagedUser(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, request.Change)
	if err != nil {
		log.Printf("[Bot] management %s failed: %v", request.Change.Kind, err)
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.changefailed", err.Error()), s.backToMainMarkup())
		return
	}
	notice := trh(s.cfg.Language, "manage.completed", msg)
	switch request.Change.Kind {
	case "delete", "disable":
		s.sendOrEdit(chatID, msgID, notice, newKeyboard(newRow(newButton(tr(s.cfg.Language, "account.list"), "menu_users"), newButton(tr(s.cfg.Language, "menu.home"), "menu_main"))))
	case "rename":
		s.sendUserManageCardNotice(chatID, msgID, userCallbackKey(request.Change.NewName), notice)
	default:
		s.sendUserManageCardNotice(chatID, msgID, userCallbackKey(request.Change.User), notice)
	}
}

func (s *BotService) handleUserManagementCommand(msg *tgMessage) bool {
	cmd := msg.Command()
	if cmd != "adduser" && cmd != "renameuser" && cmd != "rotateuser" && cmd != "removeuser" && cmd != "assignuser" && cmd != "unassignuser" && cmd != "disableuser" && cmd != "enableuser" {
		return false
	}
	parts := strings.Fields(msg.CommandArguments())
	change := UserChange{}
	switch cmd {
	case "adduser":
		if len(parts) < 1 || len(parts) > 2 {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.usage.adduser"))
			return true
		}
		change = UserChange{Kind: "add", User: parts[0]}
		if len(parts) == 2 {
			change.Node = parts[1]
		}
	case "renameuser":
		if len(parts) != 2 {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.usage.renameuser"))
			return true
		}
		change = UserChange{Kind: "rename", User: parts[0], NewName: parts[1]}
	case "disableuser", "enableuser":
		if len(parts) != 1 {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.usage.user", cmd))
			return true
		}
		kind := "disable"
		if cmd == "enableuser" {
			kind = "enable"
		}
		change = UserChange{Kind: kind, User: parts[0]}
	case "rotateuser":
		if len(parts) != 1 {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.usage.user", "rotateuser"))
			return true
		}
		change = UserChange{Kind: "rotate", User: parts[0]}
	case "removeuser":
		if len(parts) != 1 {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.usage.user", "removeuser"))
			return true
		}
		change = UserChange{Kind: "delete", User: parts[0]}
	case "assignuser", "unassignuser":
		if len(parts) != 2 {
			s.sendAlert(msg.Chat.ID, trh(s.cfg.Language, "manage.usage.node", cmd))
			return true
		}
		kind := "assign"
		if cmd == "unassignuser" {
			kind = "unassign"
		}
		change = UserChange{Kind: kind, User: parts[0], Node: parts[1]}
	}
	s.offerUserChange(msg.Chat.ID, msg.From.ID, 0, change)
	return true
}

func lookupManagedUserEmail(nodes []InboundDetails, key string) string {
	for _, email := range sortedEmails(nodes) {
		if userCallbackKey(email) == key {
			return email
		}
	}
	return ""
}

func (s *BotService) sendUserManageCard(chatID int64, msgID int, key string) {
	s.sendUserManageCardNotice(chatID, msgID, key, "")
}

// sendUserManageCardNotice shows a user's card, optionally with the outcome of
// the admin's last action above it so the next step is one tap away.
func (s *BotService) sendUserManageCardNotice(chatID int64, msgID int, key string, notice htmlText) {
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "nodes.failure", err.Error()), s.backToMainMarkup())
		return
	}
	email := lookupManagedUserByKey(s.cfg.XrayConfigPath, key)
	if email == "" {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "users.empty"), s.backToMainMarkup())
		return
	}
	var nodeRows [][]tgButton
	var content htmlBuilder
	var usedBytes uint64
	if notice != "" {
		content.Add(notice, "\n\n")
	}
	content.Add(trh(s.cfg.Language, "account.title", email))
	if root, e := decodeBindingMigrationConfig(s.cfg.XrayConfigPath); e == nil {
		if ambiguous, e := trafficAmbiguities(s.cfg.XrayConfigPath, root); e == nil && ambiguous[email] {
			content.Add(trh(s.cfg.Language, "account.traffic_ambiguous"))
		}
	}
	if state, e := loadTrafficStore(s.cfg.XrayConfigPath); e == nil {
		if c, ok := state.Users[email]; ok {
			usedBytes = c.TotalUp + c.TotalDown
			content.Add(trh(s.cfg.Language, "account.total", FormatBytes(c.TotalUp), FormatBytes(c.TotalDown), FormatBytes(usedBytes)))
			if entries := storedBindingTrafficForUser(state, email); len(entries) > 0 {
				content.Add(trh(s.cfg.Language, "account.bindings.header"))
				limit := len(entries)
				if limit > 8 {
					limit = 8
				}
				for _, item := range entries[:limit] {
					content.Add(trh(s.cfg.Language, "account.bindings.row", item.Tag, FormatBytes(item.Uplink), FormatBytes(item.Downlink), FormatBytes(item.Total())))
				}
				if len(entries) > limit {
					content.Add(trh(s.cfg.Language, "account.bindings.more", len(entries)-limit))
				}
			}
		}
		if ips, known := recentOnline(state, email, time.Now()); !known {
			content.Add(trh(s.cfg.Language, "account.unknown"))
		} else if len(ips) > 0 {
			content.Add(trh(s.cfg.Language, "account.online", len(ips)))
			for _, ip := range ips {
				content.Add("  • ", esc(ip.IP), "\n")
			}
		} else if stamp := state.LastActive[email]; stamp > 0 && time.Now().Unix()-stamp <= int64(onlineActivityWindow/time.Second) {
			content.Add(trh(s.cfg.Language, "account.active"))
		} else {
			content.Add(trh(s.cfg.Language, "account.offline"))
		}
		if stamp := state.LastOnline[email]; stamp > 0 {
			content.Add(trh(s.cfg.Language, "account.last", time.Unix(stamp, 0).Format("2006-01-02 15:04:05")))
		} else {
			content.Add(trh(s.cfg.Language, "account.lastNever"))
		}
		if entries := state.OnlineIPs[email]; len(entries) > 0 {
			content.Add(trh(s.cfg.Language, "account.recent"))
			for i, ip := range entries {
				if i >= 5 {
					break
				}
				content.Add(trh(s.cfg.Language, "account.recentline", ip.IP, time.Unix(ip.LastSeen, 0).Format("2006-01-02 15:04:05")))
			}
		}
		content.Add(trh(s.cfg.Language, "account.warning"))
	} else {
		content.Add(trh(s.cfg.Language, "account.error", e.Error()))
	}
	limits, limitErr := snapshotUserLimits(s.cfg.XrayConfigPath)
	hasLimits := false
	if limitErr != nil {
		content.Add(trh(s.cfg.Language, "account.limits.error", limitErr.Error()))
	} else if limit, ok := limits[email]; ok && !limit.empty() {
		hasLimits = true
		content.Add(trh(s.cfg.Language, "account.limits.header"))
		if limit.QuotaBytes > 0 {
			content.Add(trh(s.cfg.Language, "account.limits.quota", FormatBytes(uint64(limit.QuotaBytes)), FormatBytes(usedBytes)))
			content.Add(usageMeter(usedBytes, uint64(limit.QuotaBytes)), "\n")
		}
		if limit.ExpiryUnix > 0 {
			content.Add(trh(s.cfg.Language, "account.limits.expiry", expiryLabel(limit, s.cfg.Language)+expiryCountdown(limit, time.Now(), s.cfg.Language)))
		}
	} else {
		content.Add(trh(s.cfg.Language, "account.limits.none"))
	}
	for _, node := range nodes {
		if node.Protocol != "vless" || node.Tag == "" {
			continue
		}
		present := false
		for _, client := range node.Clients {
			if bindingOwner(client.Email) == email {
				present = true
				break
			}
		}
		mark := tr(s.cfg.Language, "account.join")
		op := "a"
		if present {
			mark = tr(s.cfg.Language, "account.leave")
			op = "r"
		}
		status := tr(s.cfg.Language, "account.unassigned")
		if present {
			status = tr(s.cfg.Language, "account.assigned")
		}
		content.Add(esc(fmt.Sprintf("• %s (%d): %s\n", node.Tag, node.Port, status)))
		nodeRows = append(nodeRows, newRow(
			newButton(mark+" "+truncateLabel(node.Tag, 28), "manage_node:"+op+":"+key+":"+userCallbackKey(node.Tag)),
		))
	}
	lang := s.cfg.Language
	// Sharing is the most frequent action; changes are hidden when remote
	// changes are disabled instead of offering buttons that always fail.
	rows := [][]tgButton{newRow(newButton(tr(lang, "account.export"), "user_links:"+key))}
	if s.cfg.AllowMutations {
		rows = append(rows, nodeRows...)
		rows = append(rows,
			newRow(newButton(tr(lang, "account.quota"), "manage_quota:"+key), newButton(tr(lang, "account.expiry"), "manage_expiry:"+key)),
			newRow(newButton(tr(lang, "account.rename"), "manage_rename:"+key), newButton(tr(lang, "account.rotate"), "manage_rotate:"+key)),
			newRow(newButton(tr(lang, "account.suspend"), "manage_disable:"+key), newButton(tr(lang, "account.trafficreset"), "manage_traffic:"+key)),
		)
		last := []tgButton{}
		if hasLimits {
			last = append(last, newButton(tr(lang, "account.limitsclear"), "manage_limitclear:"+key))
		}
		rows = append(rows, append(last, newButton(tr(lang, "account.delete"), "manage_delete:"+key)))
	}
	rows = append(rows, newRow(newButton(tr(lang, "account.list"), "menu_users"), newButton(tr(lang, "account.refresh"), "manage_user:"+key), newButton(tr(lang, "menu.home"), "menu_main")))
	content.Add(trh(s.cfg.Language, "account.footer"))
	s.sendOrEdit(chatID, msgID, content.HTML(), newKeyboard(rows...))
}

func (s *BotService) handleManagedUserCallback(cb *tgCallback) {
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
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.inboundsfailed", err.Error()))
		return
	}
	switch parts[0] {
	case "manage_user":
		s.sendUserManageCard(chatID, msgID, parts[1])
		return
	case "manage_rotate", "manage_delete", "manage_disable":
		email := lookupManagedUserByKey(s.cfg.XrayConfigPath, parts[1])
		if email == "" {
			s.sendAlert(chatID, trh(s.cfg.Language, "manage.usermissing"))
			return
		}
		kind := "rotate"
		if parts[0] == "manage_delete" {
			kind = "delete"
		}
		if parts[0] == "manage_disable" {
			kind = "disable"
		}
		s.offerUserChange(chatID, cb.From.ID, msgID, UserChange{Kind: kind, User: email})
	case "manage_quota", "manage_expiry", "manage_limitclear":
		email := lookupManagedUserByKey(s.cfg.XrayConfigPath, parts[1])
		if email == "" {
			s.sendAlert(chatID, trh(s.cfg.Language, "manage.usermissing"))
			return
		}
		if parts[0] == "manage_limitclear" {
			s.clearUserLimits(chatID, msgID, parts[1], email)
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, trh(s.cfg.Language, "manage.disabled"))
			return
		}
		limits, limitErr := snapshotUserLimits(s.cfg.XrayConfigPath)
		if limitErr != nil {
			s.sendAlert(chatID, trh(s.cfg.Language, "limit.savefailed", limitErr.Error()))
			return
		}
		limit := limits[email]
		if parts[0] == "manage_quota" {
			s.beginUserLimitWizard(chatID, cb.From.ID, msgID, email, "quota", trh(s.cfg.Language, "limit.quotaprompt", quotaLabel(limit, s.cfg.Language)))
		} else {
			s.beginUserLimitWizard(chatID, cb.From.ID, msgID, email, "expiry", trh(s.cfg.Language, "limit.expiryprompt", expiryLabel(limit, s.cfg.Language)))
		}
	case "manage_traffic":
		s.askUserTrafficReset(cb)
	case "manage_node":
		if len(parts) != 4 {
			return
		}
		email := lookupManagedUserByKey(s.cfg.XrayConfigPath, parts[2])
		if email == "" {
			s.sendAlert(chatID, trh(s.cfg.Language, "manage.usermissing"))
			return
		}
		for _, node := range nodes {
			if node.Protocol != "vless" || node.Tag == "" {
				continue
			}
			if userCallbackKey(node.Tag) != parts[3] {
				continue
			}
			kind := "assign"
			if parts[1] == "r" {
				kind = "unassign"
			} else if parts[1] != "a" {
				return
			}
			s.offerUserChange(chatID, cb.From.ID, msgID, UserChange{Kind: kind, User: email, Node: node.Tag})
			return
		}
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.inboundmissing"))
	}
}

// askUserTrafficReset confirms a per-user traffic reset. The shell menu has
// always supported this; without it a quota-suspended user could only be
// restored after clearing the quota entirely.
func (s *BotService) askUserTrafficReset(cb *tgCallback) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	key := strings.TrimPrefix(cb.Data, "manage_traffic:")
	email := lookupManagedUserByKey(s.cfg.XrayConfigPath, key)
	if email == "" {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.usermissing"))
		return
	}
	if !s.cfg.AllowMutations {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.disabled"))
		return
	}
	identity, err := managedUserIdentityFingerprint(s.cfg.XrayConfigPath, email)
	if err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "traffic.userreset.failed", err.Error()))
		return
	}
	var raw [8]byte
	if _, err = rand.Read(raw[:]); err != nil {
		s.sendAlert(chatID, trh(s.cfg.Language, "node.tokenerror"))
		return
	}
	token := hex.EncodeToString(raw[:])
	pendingTrafficResets.Lock()
	for old, item := range pendingTrafficResets.items {
		if time.Now().After(item.Expires) {
			delete(pendingTrafficResets.items, old)
		}
	}
	pendingTrafficResets.items[token] = pendingTrafficReset{ChatID: chatID, AdminID: cb.From.ID, User: email, Identity: identity, Expires: time.Now().Add(3 * time.Minute)}
	pendingTrafficResets.Unlock()
	markup := newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "common.confirm"), "manage_traffic_confirm:"+token),
			newButton(tr(s.cfg.Language, "common.cancel"), "manage_traffic_cancel:"+token),
		),
	)
	s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "traffic.userreset.confirm", email), markup)
}

func (s *BotService) resolveUserTrafficReset(cb *tgCallback, confirm bool) {
	if cb.Message == nil || cb.Message.Chat == nil || cb.From == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	token := strings.TrimPrefix(strings.TrimPrefix(cb.Data, "manage_traffic_confirm:"), "manage_traffic_cancel:")
	pendingTrafficResets.Lock()
	item, ok := pendingTrafficResets.items[token]
	if ok && item.ChatID == chatID && item.AdminID == cb.From.ID {
		delete(pendingTrafficResets.items, token)
	}
	pendingTrafficResets.Unlock()
	if ok && (item.ChatID != chatID || item.AdminID != cb.From.ID) {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || time.Now().After(item.Expires) {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.expired"), s.backToMainMarkup())
		return
	}
	email, key := item.User, userCallbackKey(item.User)
	if !confirm {
		s.sendUserManageCard(chatID, msgID, key)
		return
	}
	if !s.cfg.AllowMutations {
		s.sendAlert(chatID, trh(s.cfg.Language, "manage.disabled"))
		return
	}
	if err := resetUserTrafficWithIdentity(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, email, item.Identity); err != nil {
		log.Printf("[Bot] user traffic reset for %s failed: %v", email, err)
		s.sendUserManageCardNotice(chatID, msgID, key, trh(s.cfg.Language, "traffic.userreset.failed", err.Error()))
		return
	}
	s.sendUserManageCardNotice(chatID, msgID, key, trh(s.cfg.Language, "traffic.userreset.done", email))
}

func (s *BotService) sendDisabledUsersCard(chatID int64, msgID int) {
	names, err := disabledUserNames(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, trh(s.cfg.Language, "manage.suspended.loadfailed", err.Error()), s.backToMainMarkup())
		return
	}
	rows := [][]tgButton{}
	for _, name := range names {
		rows = append(rows, newRow(
			newButton(tr(s.cfg.Language, "manage.suspended.restore", truncateLabel(name, 40)), "manage_enable:"+userCallbackKey(name)),
		))
	}
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "manage.suspended.back"), "menu_users"),
	))
	message := trh(s.cfg.Language, "manage.suspended.title")
	if len(names) == 0 {
		message = trh(s.cfg.Language, "manage.suspended.empty")
	}
	s.sendOrEdit(chatID, msgID, message, newKeyboard(rows...))
}

func (s *BotService) enableDisabledUserCallback(cb *tgCallback) {
	if cb.Message == nil {
		return
	}
	names, err := disabledUserNames(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendAlert(cb.Message.Chat.ID, trh(s.cfg.Language, "manage.suspended.corrupt"))
		return
	}
	key := strings.TrimPrefix(cb.Data, "manage_enable:")
	for _, name := range names {
		if userCallbackKey(name) == key {
			s.offerUserChange(cb.Message.Chat.ID, cb.From.ID, cb.Message.MessageID, UserChange{Kind: "enable", User: name})
			return
		}
	}
	s.sendAlert(cb.Message.Chat.ID, trh(s.cfg.Language, "manage.suspended.notfound"))
}

func lookupManagedUserByKey(configPath, key string) string {
	names, err := allDirectoryNames(configPath)
	if err != nil {
		return ""
	}
	for _, name := range names {
		if userCallbackKey(name) == key {
			return name
		}
	}
	return ""
}
