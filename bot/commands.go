package main

import "log"

// adminCommandNames lists the commands offered in Telegram's command menu and
// on the help card, in display order. Each has a "command.<name>" description.
var adminCommandNames = []string{"start", "users", "adduser", "renameuser", "rotateuser", "removeuser", "disableuser", "enableuser", "assignuser", "unassignuser", "nodes", "traffic", "online", "onlinedoctor", "stats", "routing", "xrayconfig", "inboundtraffic", "status", "version", "update", "restart", "help", "cancel", "language"}

// commandArguments documents the argument syntax of commands that take input.
var commandArguments = map[string]string{
	"adduser":      "<user> [inbound|all]",
	"renameuser":   "<old> <new>",
	"rotateuser":   "<user>",
	"removeuser":   "<user>",
	"disableuser":  "<user>",
	"enableuser":   "<user>",
	"assignuser":   "<user> <inbound>",
	"unassignuser": "<user> <inbound>",
	"language":     "en|zh-CN",
}

func adminCommands(lang string) []tgCommand {
	commands := make([]tgCommand, 0, len(adminCommandNames))
	for _, name := range adminCommandNames {
		commands = append(commands, tgCommand{Command: name, Description: tr(lang, "command."+name)})
	}
	return commands
}

// Register Telegram slash-command suggestions only for an authorized private
// chat. This does not change access control; every command is still checked.
func (s *BotService) registerAdminCommands(chatID int64) {
	if err := s.tg.setChatCommands(chatID, adminCommands(s.cfg.Language)); err != nil {
		log.Printf("[Bot] admin command menu registration failed: %v", err)
	}
}
