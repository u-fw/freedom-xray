package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

const (
	telegramRequestTimeout = 30 * time.Second
	// Keep long polling below the HTTP client timeout so an idle poll ends
	// normally instead of as a client timeout.
	telegramPollSeconds = 25
)

// telegramClient is the only component that uses the Bot API library.
type telegramClient struct {
	api  *telego.Bot
	http *http.Client
}

// telegramLogger forwards library errors to the service log. Transport errors
// can quote request URLs, which contain the Bot token.
type telegramLogger struct{ token string }

func (telegramLogger) Debugf(string, ...any) {}

func (l telegramLogger) Errorf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if l.token != "" {
		message = strings.ReplaceAll(message, l.token, "BOT_TOKEN")
	}
	log.Printf("[Telegram] %s", message)
}

// newTelegramClient builds a client that sends every request through
// httpClient. apiServer is empty for the public Bot API.
func newTelegramClient(token, apiServer string, httpClient *http.Client) (*telegramClient, error) {
	options := []telego.BotOption{telego.WithHTTPClient(httpClient), telego.WithLogger(telegramLogger{token: token})}
	if apiServer != "" {
		options = append(options, telego.WithAPIServer(apiServer))
	}
	api, err := telego.NewBot(token, options...)
	if err != nil {
		return nil, err
	}
	return &telegramClient{api: api, http: httpClient}, nil
}

// sanitize removes the Bot token from errors before callers log or display
// them: transport failures quote the request URL, which embeds the token.
func (c *telegramClient) sanitize(err error) error {
	if err == nil {
		return nil
	}
	token := c.api.Token()
	if message := err.Error(); token != "" && strings.Contains(message, token) {
		return errors.New(strings.ReplaceAll(message, token, "BOT_TOKEN"))
	}
	return err
}

func telegramContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), telegramRequestTimeout)
}

// identity verifies the token with getMe and returns the Bot's username and ID.
func (c *telegramClient) identity() (string, int64, error) {
	ctx, cancel := telegramContext()
	defer cancel()
	user, err := c.api.GetMe(ctx)
	if err != nil {
		return "", 0, c.sanitize(err)
	}
	return user.Username, user.ID, nil
}

// updates streams message and callback updates until ctx is cancelled.
func (c *telegramClient) updates(ctx context.Context) (<-chan tgUpdate, error) {
	source, err := c.api.UpdatesViaLongPolling(ctx,
		&telego.GetUpdatesParams{Timeout: telegramPollSeconds, AllowedUpdates: []string{"message", "callback_query"}},
		telego.WithLongPollingRetryTimeout(5*time.Second))
	if err != nil {
		return nil, c.sanitize(err)
	}
	updates := make(chan tgUpdate)
	go func() {
		defer close(updates)
		for update := range source {
			converted, ok := convertTelegramUpdate(update)
			if !ok {
				continue
			}
			select {
			case updates <- converted:
			case <-ctx.Done():
				return
			}
		}
	}()
	return updates, nil
}

func convertTelegramUpdate(update telego.Update) (tgUpdate, bool) {
	if update.Message != nil {
		return tgUpdate{Message: convertTelegramMessage(update.Message)}, true
	}
	if query := update.CallbackQuery; query != nil {
		callback := &tgCallback{ID: query.ID, From: &tgUser{ID: query.From.ID, LanguageCode: query.From.LanguageCode}, Data: query.Data}
		if query.Message != nil {
			chat := query.Message.GetChat()
			callback.Message = &tgMessage{MessageID: query.Message.GetMessageID(), Chat: &tgChat{ID: chat.ID, Type: chat.Type}}
		}
		return tgUpdate{CallbackQuery: callback}, true
	}
	return tgUpdate{}, false
}

func convertTelegramMessage(message *telego.Message) *tgMessage {
	converted := &tgMessage{MessageID: message.MessageID, Chat: &tgChat{ID: message.Chat.ID, Type: message.Chat.Type}, Text: message.Text}
	if message.From != nil {
		converted.From = &tgUser{ID: message.From.ID, LanguageCode: message.From.LanguageCode}
	}
	for _, entity := range message.Entities {
		converted.Entities = append(converted.Entities, tgEntity{Type: entity.Type, Offset: entity.Offset, Length: entity.Length})
	}
	if document := message.Document; document != nil {
		converted.Document = &tgDocument{FileID: document.FileID, FileName: document.FileName, MimeType: document.MimeType, FileSize: document.FileSize}
	}
	return converted
}

func telegramMarkup(keyboard *tgKeyboard) *telego.InlineKeyboardMarkup {
	if keyboard == nil {
		return nil
	}
	rows := make([][]telego.InlineKeyboardButton, 0, len(keyboard.InlineKeyboard))
	for _, row := range keyboard.InlineKeyboard {
		buttons := make([]telego.InlineKeyboardButton, 0, len(row))
		for _, button := range row {
			converted := telego.InlineKeyboardButton{Text: button.Text, CallbackData: button.Data, URL: button.URL}
			if button.Copy != "" {
				converted.CopyText = &telego.CopyTextButton{Text: button.Copy}
			}
			buttons = append(buttons, converted)
		}
		if len(buttons) > 0 {
			rows = append(rows, buttons)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// sendText sends a message and returns its ID. An empty parseMode sends plain text.
func (c *telegramClient) sendText(chatID int64, text, parseMode string, keyboard *tgKeyboard, disablePreview bool) (int, error) {
	ctx, cancel := telegramContext()
	defer cancel()
	params := &telego.SendMessageParams{ChatID: tu.ID(chatID), Text: text, ParseMode: parseMode}
	// A typed nil inside the ReplyMarkup interface would be sent as null.
	if markup := telegramMarkup(keyboard); markup != nil {
		params.ReplyMarkup = markup
	}
	if disablePreview {
		params.LinkPreviewOptions = &telego.LinkPreviewOptions{IsDisabled: true}
	}
	message, err := c.api.SendMessage(ctx, params)
	if err != nil {
		return 0, c.sanitize(err)
	}
	return message.MessageID, nil
}

// editText replaces a message's text and keyboard; a nil keyboard removes it.
func (c *telegramClient) editText(chatID int64, messageID int, text, parseMode string, keyboard *tgKeyboard) error {
	ctx, cancel := telegramContext()
	defer cancel()
	_, err := c.api.EditMessageText(ctx, &telego.EditMessageTextParams{ChatID: tu.ID(chatID), MessageID: messageID, Text: text, ParseMode: parseMode, ReplyMarkup: telegramMarkup(keyboard)})
	return c.sanitize(err)
}

func (c *telegramClient) answerCallback(callbackID, text string) error {
	ctx, cancel := telegramContext()
	defer cancel()
	return c.sanitize(c.api.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: text}))
}

func (c *telegramClient) deleteMessage(chatID int64, messageID int) error {
	ctx, cancel := telegramContext()
	defer cancel()
	return c.sanitize(c.api.DeleteMessage(ctx, &telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: messageID}))
}

// sendDocument uploads data as a file. The caption is plain text.
func (c *telegramClient) sendDocument(chatID int64, name string, data []byte, caption string) error {
	ctx, cancel := telegramContext()
	defer cancel()
	_, err := c.api.SendDocument(ctx, &telego.SendDocumentParams{ChatID: tu.ID(chatID), Document: tu.File(tu.NameReader(bytes.NewReader(data), name)), Caption: caption})
	return c.sanitize(err)
}

// sendPhoto uploads an image. The caption is plain text.
func (c *telegramClient) sendPhoto(chatID int64, name string, data []byte, caption string) error {
	ctx, cancel := telegramContext()
	defer cancel()
	_, err := c.api.SendPhoto(ctx, &telego.SendPhotoParams{ChatID: tu.ID(chatID), Photo: tu.File(tu.NameReader(bytes.NewReader(data), name)), Caption: caption})
	return c.sanitize(err)
}

// setChatCommands replaces the command suggestions shown in one chat.
func (c *telegramClient) setChatCommands(chatID int64, commands []tgCommand) error {
	converted := make([]telego.BotCommand, 0, len(commands))
	for _, command := range commands {
		converted = append(converted, telego.BotCommand{Command: command.Command, Description: command.Description})
	}
	ctx, cancel := telegramContext()
	defer cancel()
	return c.sanitize(c.api.SetMyCommands(ctx, &telego.SetMyCommandsParams{Commands: converted, Scope: &telego.BotCommandScopeChat{Type: telego.ScopeTypeChat, ChatID: tu.ID(chatID)}}))
}

// fileURL resolves a document to its download path and URL. The URL embeds the
// Bot token and must never reach logs or chat messages.
func (c *telegramClient) fileURL(fileID string) (filePath string, size int64, url string, err error) {
	ctx, cancel := telegramContext()
	defer cancel()
	file, err := c.api.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return "", 0, "", c.sanitize(err)
	}
	return file.FilePath, file.FileSize, c.api.FileDownloadURL(file.FilePath), nil
}
