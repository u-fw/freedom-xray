package main

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Service actions such as an Xray restart are confirmed with a one-time token
// bound to the action, chat and admin. An old confirmation button left in the
// chat history therefore cannot trigger the action again later.
var actionConfirmations = struct {
	sync.Mutex
	items map[string]actionConfirmation
}{items: map[string]actionConfirmation{}}

type actionConfirmation struct {
	Action  string
	ChatID  int64
	AdminID int64
	Expires time.Time
}

const actionConfirmationLifetime = 3 * time.Minute

// confirmData returns callback data for a confirm button of action.
func confirmData(prefix, action string, chatID, adminID int64) string {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		// Without a token the button can only report an expired confirmation.
		return prefix
	}
	token := hex.EncodeToString(nonce[:])
	now := time.Now()
	actionConfirmations.Lock()
	defer actionConfirmations.Unlock()
	for key, item := range actionConfirmations.items {
		if now.After(item.Expires) {
			delete(actionConfirmations.items, key)
		}
	}
	actionConfirmations.items[token] = actionConfirmation{Action: action, ChatID: chatID, AdminID: adminID, Expires: now.Add(actionConfirmationLifetime)}
	return prefix + ":" + token
}

// takeConfirmation consumes the token carried by a confirm button. It reports
// false, and tells the admin, when the confirmation is unknown, expired, used,
// or belongs to another chat, admin or action.
func (s *BotService) takeConfirmation(cb *tgCallback, prefix, action string) bool {
	token := strings.TrimPrefix(strings.TrimPrefix(cb.Data, prefix), ":")
	chatID := cb.Message.Chat.ID
	actionConfirmations.Lock()
	item, ok := actionConfirmations.items[token]
	valid := ok && item.Action == action && item.ChatID == chatID && item.AdminID == cb.From.ID && time.Now().Before(item.Expires)
	if valid {
		delete(actionConfirmations.items, token)
	}
	actionConfirmations.Unlock()
	if !valid {
		s.sendOrEdit(chatID, cb.Message.MessageID, trh(s.cfg.Language, "confirm.expired"), s.backToMainMarkup())
	}
	return valid
}
