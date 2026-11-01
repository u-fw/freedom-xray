package main

// clearInputSessions ends every pending text-input flow for a chat. A chat's
// next message may belong to only one flow, so starting, finishing or leaving
// any flow clears them all.
func clearInputSessions(chatID int64) {
	clearNodeCreateState(chatID)
	nodePathWizards.Lock()
	delete(nodePathWizards.items, chatID)
	nodePathWizards.Unlock()
	userLimitWizards.Lock()
	delete(userLimitWizards.items, chatID)
	userLimitWizards.Unlock()
	managementWizards.Lock()
	delete(managementWizards.states, chatID)
	managementWizards.Unlock()
	routingWizards.Lock()
	delete(routingWizards.items, chatID)
	routingWizards.Unlock()
	clearGuidedRoutingWizard(chatID)
	clearBalancerWizard(chatID)
	clearOutboundWizard(chatID)
	clearDNSWizard(chatID)
	clearNativeInboundWizard(chatID)
}

// retryInput reports invalid input while the flow stays open for a correction.
func (s *BotService) retryInput(chatID int64, problem htmlText) {
	s.sendAlert(chatID, problem+"\n"+trh(s.cfg.Language, "wizard.retry"))
}
