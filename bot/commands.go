package main

import "log"

// Register Telegram slash-command suggestions only for an authorized private
// chat. This does not change access control; every command is still checked.
func (s *BotService) registerAdminCommands(chatID int64) {
	commands := []tgCommand{
		{Command: "start", Description: tr(s.cfg.Language, "command.start")},
		{Command: "users", Description: tr(s.cfg.Language, "command.users")},
		{Command: "adduser", Description: tr(s.cfg.Language, "command.adduser")},
		{Command: "renameuser", Description: tr(s.cfg.Language, "command.renameuser")},
		{Command: "rotateuser", Description: tr(s.cfg.Language, "command.rotateuser")},
		{Command: "removeuser", Description: tr(s.cfg.Language, "command.removeuser")},
		{Command: "disableuser", Description: tr(s.cfg.Language, "command.disableuser")},
		{Command: "enableuser", Description: tr(s.cfg.Language, "command.enableuser")},
		{Command: "assignuser", Description: tr(s.cfg.Language, "command.assignuser")},
		{Command: "unassignuser", Description: tr(s.cfg.Language, "command.unassignuser")},
		{Command: "nodes", Description: tr(s.cfg.Language, "command.nodes")},
		{Command: "traffic", Description: tr(s.cfg.Language, "command.traffic")},
		{Command: "online", Description: tr(s.cfg.Language, "command.online")},
		{Command: "onlinedoctor", Description: tr(s.cfg.Language, "command.onlinedoctor")},
		{Command: "stats", Description: tr(s.cfg.Language, "command.stats")},
		{Command: "routing", Description: tr(s.cfg.Language, "command.routing")},
		{Command: "xrayconfig", Description: tr(s.cfg.Language, "command.xrayconfig")},
		{Command: "inboundtraffic", Description: tr(s.cfg.Language, "command.inboundtraffic")},
		{Command: "status", Description: tr(s.cfg.Language, "command.status")},
		{Command: "version", Description: tr(s.cfg.Language, "command.version")},
		{Command: "update", Description: tr(s.cfg.Language, "command.update")},
		{Command: "restart", Description: tr(s.cfg.Language, "command.restart")},
		{Command: "help", Description: tr(s.cfg.Language, "command.help")},
		{Command: "cancel", Description: tr(s.cfg.Language, "command.cancel")},
		{Command: "language", Description: tr(s.cfg.Language, "command.language")},
	}
	if err := s.tg.setChatCommands(chatID, commands); err != nil {
		log.Printf("[Bot] admin command menu registration failed: %v", err)
	}
}
