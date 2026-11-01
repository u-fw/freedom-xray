package main

import (
	"strings"
	"unicode"
)

// Library-independent Telegram values. Handlers, menus and tests use these
// types; telegram_client.go is the only place that converts them to and from
// the Bot API client library.

type tgUser struct {
	ID           int64
	LanguageCode string
}

type tgChat struct {
	ID   int64
	Type string
}

// IsPrivate reports a one-to-one chat between a user and the Bot.
func (c *tgChat) IsPrivate() bool { return c != nil && c.Type == "private" }

type tgDocument struct {
	FileID   string
	FileName string
	MimeType string
	FileSize int64
}

type tgEntity struct {
	Type   string
	Offset int
	Length int
}

type tgMessage struct {
	MessageID int
	From      *tgUser
	Chat      *tgChat
	Text      string
	Entities  []tgEntity
	Document  *tgDocument
}

// IsCommand reports whether the message starts with a bot_command entity.
// Command names are ASCII, so the entity's UTF-16 length is also its byte length.
func (m *tgMessage) IsCommand() bool {
	if m == nil || len(m.Entities) == 0 {
		return false
	}
	entity := m.Entities[0]
	return entity.Type == "bot_command" && entity.Offset == 0 && entity.Length > 1 && entity.Length <= len(m.Text)
}

// Command returns the command name without its slash or @bot suffix.
func (m *tgMessage) Command() string {
	if !m.IsCommand() {
		return ""
	}
	command := m.Text[1:m.Entities[0].Length]
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	return command
}

// CommandArguments returns the text that follows the command.
func (m *tgMessage) CommandArguments() string {
	if !m.IsCommand() {
		return ""
	}
	return strings.TrimLeftFunc(m.Text[m.Entities[0].Length:], unicode.IsSpace)
}

type tgCallback struct {
	ID string
	// Message is nil when Telegram no longer exposes the original message.
	Message *tgMessage
	From    *tgUser
	Data    string
}

type tgUpdate struct {
	Message       *tgMessage
	CallbackQuery *tgCallback
}

// tgButton is an inline keyboard button. Data is callback data; Copy, when
// set, makes the button copy that text to the clipboard instead.
type tgButton struct {
	Text string
	Data string
	URL  string
	Copy string
}

type tgKeyboard struct {
	InlineKeyboard [][]tgButton
}

func newButton(text, data string) tgButton { return tgButton{Text: text, Data: data} }

func newRow(buttons ...tgButton) []tgButton { return buttons }

func newKeyboard(rows ...[]tgButton) tgKeyboard { return tgKeyboard{InlineKeyboard: rows} }

type tgCommand struct {
	Command     string
	Description string
}
