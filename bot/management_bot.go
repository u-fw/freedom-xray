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
		s.sendOrEdit(chatID, messageID, tr(s.cfg.Language, "manage.disabled"), s.backToMainMarkup())
		return
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.tokenerror", err.Error()))
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
		if change.Node == "" {
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
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.unknown"))
		return
	}
	markup := newKeyboard(
		newRow(
			newButton(tr(s.cfg.Language, "manage.confirm"), "manage_confirm:"+key),
			newButton(tr(s.cfg.Language, "common.cancel"), "manage_cancel:"+key),
		),
	)
	s.sendOrEdit(chatID, messageID, tr(s.cfg.Language, "manage.confirmbody", description), markup)
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
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || time.Now().After(request.ExpiresAt) {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.expired"), s.backToMainMarkup())
		return
	}
	if !confirm {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.cancelled"), s.backToMainMarkup())
		return
	}
	if !s.cfg.AllowMutations {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.writesdisabled"), s.backToMainMarkup())
		return
	}
	msg, err := changeManagedUser(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, request.Change)
	if err != nil {
		log.Printf("[Bot] management %s failed: %v", request.Change.Kind, err)
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.changefailed", err.Error()), s.backToMainMarkup())
		return
	}
	s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.completed", msg), s.backToMainMarkup())
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
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "manage.usage.adduser"))
			return true
		}
		change = UserChange{Kind: "add", User: parts[0]}
		if len(parts) == 2 {
			change.Node = parts[1]
		}
	case "renameuser":
		if len(parts) != 2 {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "manage.usage.renameuser"))
			return true
		}
		change = UserChange{Kind: "rename", User: parts[0], NewName: parts[1]}
	case "disableuser", "enableuser":
		if len(parts) != 1 {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "manage.usage.user", cmd))
			return true
		}
		kind := "disable"
		if cmd == "enableuser" {
			kind = "enable"
		}
		change = UserChange{Kind: kind, User: parts[0]}
	case "rotateuser":
		if len(parts) != 1 {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "manage.usage.user", "rotateuser"))
			return true
		}
		change = UserChange{Kind: "rotate", User: parts[0]}
	case "removeuser":
		if len(parts) != 1 {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "manage.usage.user", "removeuser"))
			return true
		}
		change = UserChange{Kind: "delete", User: parts[0]}
	case "assignuser", "unassignuser":
		if len(parts) != 2 {
			s.sendAlert(msg.Chat.ID, tr(s.cfg.Language, "manage.usage.node", cmd))
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
	nodes, err := LoadXrayInbounds(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "nodes.failure", err.Error()), s.backToMainMarkup())
		return
	}
	email := lookupManagedUserByKey(s.cfg.XrayConfigPath, key)
	if email == "" {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "users.empty"), s.backToMainMarkup())
		return
	}
	var rows [][]tgButton
	var content strings.Builder
	var usedBytes uint64
	content.WriteString(tr(s.cfg.Language, "account.title", email))
	if root, e := decodeBindingMigrationConfig(s.cfg.XrayConfigPath); e == nil {
		if ambiguous, e := trafficAmbiguities(s.cfg.XrayConfigPath, root); e == nil && ambiguous[email] {
			content.WriteString(tr(s.cfg.Language, "account.traffic_ambiguous"))
		}
	}
	if state, e := loadTrafficStore(s.cfg.XrayConfigPath); e == nil {
		if c, ok := state.Users[email]; ok {
			usedBytes = c.TotalUp + c.TotalDown
			content.WriteString(tr(s.cfg.Language, "account.total", FormatBytes(c.TotalUp), FormatBytes(c.TotalDown), FormatBytes(usedBytes)))
			if entries := storedBindingTrafficForUser(state, email); len(entries) > 0 {
				content.WriteString(tr(s.cfg.Language, "account.bindings.header"))
				limit := len(entries)
				if limit > 8 {
					limit = 8
				}
				for _, item := range entries[:limit] {
					content.WriteString(tr(s.cfg.Language, "account.bindings.row", mdEsc(item.Tag), FormatBytes(item.Uplink), FormatBytes(item.Downlink), FormatBytes(item.Total())))
				}
				if len(entries) > limit {
					content.WriteString(tr(s.cfg.Language, "account.bindings.more", len(entries)-limit))
				}
			}
		}
		if ips, known := recentOnline(state, email, time.Now()); !known {
			content.WriteString(tr(s.cfg.Language, "account.unknown"))
		} else if len(ips) > 0 {
			content.WriteString(tr(s.cfg.Language, "account.online", len(ips)))
			for _, ip := range ips {
				content.WriteString("  • " + ip.IP + "\n")
			}
		} else if stamp := state.LastActive[email]; stamp > 0 && time.Now().Unix()-stamp <= int64(onlineActivityWindow/time.Second) {
			content.WriteString(tr(s.cfg.Language, "account.active"))
		} else {
			content.WriteString(tr(s.cfg.Language, "account.offline"))
		}
		if stamp := state.LastOnline[email]; stamp > 0 {
			content.WriteString(tr(s.cfg.Language, "account.last", time.Unix(stamp, 0).Format("2006-01-02 15:04:05")))
		} else {
			content.WriteString(tr(s.cfg.Language, "account.lastNever"))
		}
		if entries := state.OnlineIPs[email]; len(entries) > 0 {
			content.WriteString(tr(s.cfg.Language, "account.recent"))
			for i, ip := range entries {
				if i >= 5 {
					break
				}
				content.WriteString(tr(s.cfg.Language, "account.recentline", mdCode(ip.IP), time.Unix(ip.LastSeen, 0).Format("2006-01-02 15:04:05")))
			}
		}
		content.WriteString(tr(s.cfg.Language, "account.warning"))
	} else {
		content.WriteString(tr(s.cfg.Language, "account.error", e.Error()))
	}
	limits, limitErr := snapshotUserLimits(s.cfg.XrayConfigPath)
	if limitErr != nil {
		content.WriteString(tr(s.cfg.Language, "account.limits.error", mdEsc(limitErr.Error())))
	} else if limit, ok := limits[email]; ok && !limit.empty() {
		content.WriteString(tr(s.cfg.Language, "account.limits.header"))
		if limit.QuotaBytes > 0 {
			content.WriteString(tr(s.cfg.Language, "account.limits.quota", mdCode(FormatBytes(uint64(limit.QuotaBytes))), mdCode(FormatBytes(usedBytes))))
		}
		if limit.ExpiryUnix > 0 {
			content.WriteString(tr(s.cfg.Language, "account.limits.expiry", mdCode(expiryLabel(limit, s.cfg.Language))))
		}
	} else {
		content.WriteString(tr(s.cfg.Language, "account.limits.none"))
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
		content.WriteString(fmt.Sprintf("• %s (%d): %s\n", node.Tag, node.Port, status))
		rows = append(rows, newRow(
			newButton(mark+" "+truncateLabel(node.Tag, 28), "manage_node:"+op+":"+key+":"+userCallbackKey(node.Tag)),
		))
	}
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "account.refresh"), "manage_user:"+key),
		newButton(tr(s.cfg.Language, "account.export"), "user_links:"+key),
		newButton(tr(s.cfg.Language, "account.rename"), "manage_rename:"+key),
	))
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "account.rotate"), "manage_rotate:"+key),
	))
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "account.suspend"), "manage_disable:"+key),
	))
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "account.quota"), "manage_quota:"+key),
		newButton(tr(s.cfg.Language, "account.expiry"), "manage_expiry:"+key),
	))
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "account.limitsclear"), "manage_limitclear:"+key),
	))
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "account.trafficreset"), "manage_traffic:"+key),
	))
	rows = append(rows, newRow(
		newButton(tr(s.cfg.Language, "account.delete"), "manage_delete:"+key),
		newButton(tr(s.cfg.Language, "account.list"), "menu_users"),
	))
	content.WriteString(tr(s.cfg.Language, "account.footer"))
	s.sendOrEdit(chatID, msgID, content.String(), newKeyboard(rows...))
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
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.inboundsfailed", err.Error()))
		return
	}
	switch parts[0] {
	case "manage_user":
		s.sendUserManageCard(chatID, msgID, parts[1])
		return
	case "manage_rotate", "manage_delete", "manage_disable":
		email := lookupManagedUserByKey(s.cfg.XrayConfigPath, parts[1])
		if email == "" {
			s.sendAlert(chatID, tr(s.cfg.Language, "manage.usermissing"))
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
			s.sendAlert(chatID, tr(s.cfg.Language, "manage.usermissing"))
			return
		}
		if parts[0] == "manage_limitclear" {
			s.clearUserLimits(chatID, msgID, parts[1], email)
			return
		}
		if !s.cfg.AllowMutations {
			s.sendAlert(chatID, tr(s.cfg.Language, "manage.disabled"))
			return
		}
		limits, limitErr := snapshotUserLimits(s.cfg.XrayConfigPath)
		if limitErr != nil {
			s.sendAlert(chatID, tr(s.cfg.Language, "limit.savefailed", limitErr.Error()))
			return
		}
		limit := limits[email]
		if parts[0] == "manage_quota" {
			s.beginUserLimitWizard(chatID, cb.From.ID, msgID, email, "quota", tr(s.cfg.Language, "limit.quotaprompt", mdCode(quotaLabel(limit, s.cfg.Language))))
		} else {
			s.beginUserLimitWizard(chatID, cb.From.ID, msgID, email, "expiry", tr(s.cfg.Language, "limit.expiryprompt", mdCode(expiryLabel(limit, s.cfg.Language))))
		}
	case "manage_traffic":
		s.askUserTrafficReset(cb)
	case "manage_node":
		if len(parts) != 4 {
			return
		}
		email := lookupManagedUserByKey(s.cfg.XrayConfigPath, parts[2])
		if email == "" {
			s.sendAlert(chatID, tr(s.cfg.Language, "manage.usermissing"))
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
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.inboundmissing"))
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
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.usermissing"))
		return
	}
	if !s.cfg.AllowMutations {
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.disabled"))
		return
	}
	identity, err := managedUserIdentityFingerprint(s.cfg.XrayConfigPath, email)
	if err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "traffic.userreset.failed", err.Error()))
		return
	}
	var raw [8]byte
	if _, err = rand.Read(raw[:]); err != nil {
		s.sendAlert(chatID, tr(s.cfg.Language, "node.tokenerror"))
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
	s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "traffic.userreset.confirm", mdCode(email)), markup)
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
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.owned"))
		return
	}
	if !ok || time.Now().After(item.Expires) {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.expired"), s.backToMainMarkup())
		return
	}
	email, key := item.User, userCallbackKey(item.User)
	if !confirm {
		s.sendUserManageCard(chatID, msgID, key)
		return
	}
	if !s.cfg.AllowMutations {
		s.sendAlert(chatID, tr(s.cfg.Language, "manage.disabled"))
		return
	}
	if err := resetUserTrafficWithIdentity(s.cfg.XrayConfigPath, s.cfg.XrayBinPath, s.cfg.StatsAPIServer, email, item.Identity); err != nil {
		log.Printf("[Bot] user traffic reset for %s failed: %v", email, err)
		s.sendAlert(chatID, tr(s.cfg.Language, "traffic.userreset.failed", err.Error()))
		s.sendUserManageCard(chatID, msgID, key)
		return
	}
	s.sendAlert(chatID, tr(s.cfg.Language, "traffic.userreset.done", email))
	s.sendUserManageCard(chatID, msgID, key)
}

func (s *BotService) sendDisabledUsersCard(chatID int64, msgID int) {
	names, err := disabledUserNames(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendOrEdit(chatID, msgID, tr(s.cfg.Language, "manage.suspended.loadfailed", err.Error()), s.backToMainMarkup())
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
	message := tr(s.cfg.Language, "manage.suspended.title")
	if len(names) == 0 {
		message = tr(s.cfg.Language, "manage.suspended.empty")
	}
	s.sendOrEdit(chatID, msgID, message, newKeyboard(rows...))
}

func (s *BotService) enableDisabledUserCallback(cb *tgCallback) {
	if cb.Message == nil {
		return
	}
	names, err := disabledUserNames(s.cfg.XrayConfigPath)
	if err != nil {
		s.sendAlert(cb.Message.Chat.ID, tr(s.cfg.Language, "manage.suspended.corrupt"))
		return
	}
	key := strings.TrimPrefix(cb.Data, "manage_enable:")
	for _, name := range names {
		if userCallbackKey(name) == key {
			s.offerUserChange(cb.Message.Chat.ID, cb.From.ID, cb.Message.MessageID, UserChange{Kind: "enable", User: name})
			return
		}
	}
	s.sendAlert(cb.Message.Chat.ID, tr(s.cfg.Language, "manage.suspended.notfound"))
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
