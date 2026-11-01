package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Well-formed fake tokens; the client rejects tokens that do not look real.
const (
	testBotToken       = "123456:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testSecretBotToken = "424242:secretTESTtokenSECRETtestTOKENsecre"
)

func newTestTelegramClient(t *testing.T, token, serverURL string, client *http.Client) *telegramClient {
	t.Helper()
	tg, err := newTelegramClient(token, serverURL, client)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

// telegramRequestValues returns Bot API parameters whether they arrived as
// JSON or as a (multipart) form. Object parameters such as reply_markup are
// returned in their JSON encoding.
func telegramRequestValues(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	values := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Telegram request: %v", err)
			return values
		}
		for key, raw := range body {
			var text string
			if json.Unmarshal(raw, &text) == nil {
				values[key] = text
			} else {
				values[key] = string(raw)
			}
		}
		return values
	}
	if err := r.ParseMultipartForm(4 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		t.Errorf("parse Telegram request: %v", err)
	}
	for key := range r.Form {
		values[key] = r.FormValue(key)
	}
	return values
}

// decodeWireKeyboard converts a captured reply_markup into the internal
// keyboard type so tests can inspect the buttons that were actually sent.
func decodeWireKeyboard(raw string) (tgKeyboard, error) {
	var wire struct {
		InlineKeyboard [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
			URL          string `json:"url"`
			CopyText     *struct {
				Text string `json:"text"`
			} `json:"copy_text"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return tgKeyboard{}, err
	}
	var keyboard tgKeyboard
	for _, row := range wire.InlineKeyboard {
		buttons := make([]tgButton, 0, len(row))
		for _, button := range row {
			converted := tgButton{Text: button.Text, Data: button.CallbackData, URL: button.URL}
			if button.CopyText != nil {
				converted.Copy = button.CopyText.Text
			}
			buttons = append(buttons, converted)
		}
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, buttons)
	}
	return keyboard, nil
}
