package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed locales/en.json locales/zh-CN.json
var localeFS embed.FS

var languageBundles = loadLanguageBundles()

func loadLanguageBundles() map[string]map[string]string {
	bundles := map[string]map[string]string{}
	for _, lang := range []string{"en", "zh-CN"} {
		b, err := localeFS.ReadFile("locales/" + lang + ".json")
		if err != nil {
			panic(err)
		}
		entries := map[string]string{}
		if err = json.Unmarshal(b, &entries); err != nil {
			panic(err)
		}
		bundles[lang] = entries
	}
	return bundles
}
func normalizeLanguage(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "zh", "zh-cn", "zh-hans":
		return "zh-CN"
	default:
		return "en"
	}
}
func supportedLanguage(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "en", "en-us", "en-gb", "zh", "zh-cn", "zh-hans":
		return true
	default:
		return false
	}
}
func tr(lang, key string, args ...any) string {
	lang = normalizeLanguage(lang)
	msg, ok := languageBundles[lang][key]
	if !ok {
		msg, ok = languageBundles["en"][key]
	}
	if !ok {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(msg, args...)
	}
	return msg
}
