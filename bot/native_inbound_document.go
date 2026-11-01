package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const telegramJSONDocumentLimit = 1 << 20

var telegramDocumentPath = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// Telegram download URLs contain the Bot token. Reject redirects and never
// return network errors or URLs to callers, logs or chat messages.
func readTelegramJSONDocument(bot *tgbotapi.BotAPI, document *tgbotapi.Document) ([]byte, error) {
	if document == nil || document.FileID == "" {
		return nil, errors.New("a JSON document is required")
	}
	if document.FileSize > telegramJSONDocumentLimit {
		return nil, errors.New("JSON document exceeds 1 MiB")
	}
	client := &http.Client{}
	if configured, ok := bot.Client.(*http.Client); ok {
		*client = *configured
	}
	if client.Timeout == 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	api := *bot
	api.Client = client
	api.Debug = false
	file, err := api.GetFile(tgbotapi.FileConfig{FileID: document.FileID})
	if err != nil || file.FileSize > telegramJSONDocumentLimit {
		return nil, errors.New("unable to retrieve the JSON document")
	}
	filePath := file.FilePath
	if len(filePath) > 1024 || !telegramDocumentPath.MatchString(filePath) || strings.HasPrefix(filePath, "/") || path.Clean(filePath) != filePath || filePath == "." {
		return nil, errors.New("unable to retrieve the JSON document")
	}
	for _, part := range strings.Split(filePath, "/") {
		if part == "." || part == ".." {
			return nil, errors.New("unable to retrieve the JSON document")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, file.Link(bot.Token), nil)
	if err != nil {
		return nil, errors.New("unable to retrieve the JSON document")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("unable to retrieve the JSON document")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > telegramJSONDocumentLimit {
		return nil, errors.New("unable to retrieve the JSON document")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, telegramJSONDocumentLimit+1))
	if err != nil {
		return nil, errors.New("unable to retrieve the JSON document")
	}
	if len(data) > telegramJSONDocumentLimit {
		return nil, errors.New("JSON document exceeds 1 MiB")
	}
	return data, nil
}
