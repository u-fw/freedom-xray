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
)

const telegramJSONDocumentLimit = 1 << 20

var telegramDocumentPath = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// Telegram download URLs contain the Bot token. Reject redirects and never
// return network errors or URLs to callers, logs or chat messages.
func readTelegramJSONDocument(tg *telegramClient, document *tgDocument) ([]byte, error) {
	if document == nil || document.FileID == "" {
		return nil, errors.New("a JSON document is required")
	}
	if document.FileSize > telegramJSONDocumentLimit {
		return nil, errors.New("JSON document exceeds 1 MiB")
	}
	filePath, fileSize, url, err := tg.fileURL(document.FileID)
	if err != nil || fileSize > telegramJSONDocumentLimit {
		return nil, errors.New("unable to retrieve the JSON document")
	}
	if len(filePath) > 1024 || !telegramDocumentPath.MatchString(filePath) || strings.HasPrefix(filePath, "/") || path.Clean(filePath) != filePath || filePath == "." {
		return nil, errors.New("unable to retrieve the JSON document")
	}
	for _, part := range strings.Split(filePath, "/") {
		if part == "." || part == ".." {
			return nil, errors.New("unable to retrieve the JSON document")
		}
	}
	client := &http.Client{}
	if tg.http != nil {
		*client = *tg.http
	}
	if client.Timeout == 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
