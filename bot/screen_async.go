package main

import "sync"

// Every send or edit records a render number for its message. Slow background
// work captures the number when it starts and replaces the message only if no
// other screen has been shown there since, so a late result cannot overwrite
// the screen an admin moved on to.
var screenRenders = struct {
	sync.Mutex
	next  uint64
	items map[chunkAnchor]uint64
}{items: map[chunkAnchor]uint64{}}

// backgroundWork tracks slow screens and updates still running; tests wait on it.
var backgroundWork sync.WaitGroup

func markScreenRender(chatID int64, msgID int) uint64 {
	screenRenders.Lock()
	defer screenRenders.Unlock()
	if len(screenRenders.items) >= maxChunkAnchors {
		// Forgetting old messages only suppresses their late results.
		screenRenders.items = map[chunkAnchor]uint64{}
	}
	screenRenders.next++
	screenRenders.items[chunkAnchor{ChatID: chatID, MsgID: int64(msgID)}] = screenRenders.next
	return screenRenders.next
}

func screenRenderCurrent(chatID int64, msgID int, render uint64) bool {
	screenRenders.Lock()
	defer screenRenders.Unlock()
	return render != 0 && screenRenders.items[chunkAnchor{ChatID: chatID, MsgID: int64(msgID)}] == render
}

// renderLater shows waiting in place of a slow screen, builds the screen in the
// background and shows it unless the admin has opened another screen there.
func (s *BotService) renderLater(chatID int64, msgID int, waiting htmlText, build func() (htmlText, tgKeyboard)) {
	anchor, render := s.showScreen(chatID, msgID, waiting, s.backToMainMarkup())
	if anchor == 0 {
		return
	}
	backgroundWork.Add(1)
	go func() {
		defer backgroundWork.Done()
		text, markup := build()
		if screenRenderCurrent(chatID, anchor, render) {
			s.sendOrEdit(chatID, anchor, text, markup)
		}
	}()
}

// deliverLater runs work whose outcome the admin must see, such as an update.
// The result replaces the waiting message while it is still shown and arrives
// as a new message otherwise.
func (s *BotService) deliverLater(chatID int64, msgID int, waiting htmlText, work func() (htmlText, tgKeyboard)) {
	anchor, render := s.showScreen(chatID, msgID, waiting, tgKeyboard{})
	backgroundWork.Add(1)
	go func() {
		defer backgroundWork.Done()
		text, markup := work()
		if anchor != 0 && screenRenderCurrent(chatID, anchor, render) {
			s.sendOrEdit(chatID, anchor, text, markup)
			return
		}
		s.sendOrEdit(chatID, 0, text, markup)
	}()
}
