package main

import (
	"fmt"
	"html"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// htmlText is Telegram HTML that is safe to send: literal text is escaped and
// tags come only from locale markers or the helpers in this file. Message
// senders accept only this type, so unescaped text cannot reach them by accident.
type htmlText string

// Format prints the visible text when HTML reaches fmt, for example as a tr
// argument, a log line or an error; %q keeps the markup for test output.
// Compose HTML with concatenation, joinHTML, htmlBuilder or trh instead.
func (h htmlText) Format(state fmt.State, verb rune) {
	if verb == 'q' {
		fmt.Fprintf(state, "%q", string(h))
		return
	}
	fmt.Fprint(state, plainHTML(h))
}

// esc escapes literal text, such as user names, tags or error messages.
func esc(text string) htmlText { return htmlText(html.EscapeString(text)) }

func bold(text string) htmlText { return "<b>" + esc(text) + "</b>" }

func code(text string) htmlText { return "<code>" + esc(text) + "</code>" }

func pre(text string) htmlText { return "<pre>" + esc(text) + "</pre>" }

func joinHTML(parts []htmlText, separator htmlText) htmlText {
	var out strings.Builder
	for i, part := range parts {
		if i > 0 {
			out.WriteString(string(separator))
		}
		out.WriteString(string(part))
	}
	return htmlText(out.String())
}

// htmlBuilder accumulates a message body without accepting raw strings.
type htmlBuilder struct{ out strings.Builder }

func (b *htmlBuilder) Add(parts ...htmlText) {
	for _, part := range parts {
		b.out.WriteString(string(part))
	}
}

func (b *htmlBuilder) Len() int { return b.out.Len() }

func (b *htmlBuilder) HTML() htmlText { return htmlText(b.out.String()) }

// Locale templates mark *bold*, `code` and ```blocks``` as in legacy Markdown.
// Every other character is literal; underscores in particular never start
// italics. An unpaired marker is kept as text.
func templateMarkup(template string, html bool) string {
	var out strings.Builder
	literal := func(text string) {
		if html {
			text = htmlEscaper.Replace(text)
		}
		out.WriteString(text)
	}
	span := func(tag, text string) {
		if html {
			out.WriteString("<" + tag + ">" + htmlEscaper.Replace(text) + "</" + tag + ">")
		} else {
			out.WriteString(text)
		}
	}
	for template != "" {
		switch {
		case strings.HasPrefix(template, "```"):
			end := strings.Index(template[3:], "```")
			if end < 0 {
				literal("```")
				template = template[3:]
				continue
			}
			span("pre", strings.TrimPrefix(template[3:3+end], "\n"))
			template = template[3+end+3:]
		case template[0] == '`' || template[0] == '*':
			end := strings.IndexByte(template[1:], template[0])
			if end < 0 {
				literal(template[:1])
				template = template[1:]
				continue
			}
			tag := "b"
			if template[0] == '`' {
				tag = "code"
			}
			span(tag, template[1:1+end])
			template = template[1+end+1:]
		default:
			next := strings.IndexAny(template, "*`")
			if next < 0 {
				next = len(template)
			}
			literal(template[:next])
			template = template[next:]
		}
	}
	return out.String()
}

// Telegram HTML accepts these entities; percent signs stay intact for Sprintf.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func localeTemplate(lang, key string) (string, bool) {
	lang = normalizeLanguage(lang)
	template, ok := languageBundles[lang][key]
	if !ok {
		template, ok = languageBundles["en"][key]
	}
	return template, ok
}

// trh renders a locale template as Telegram HTML. String-like arguments are
// escaped; htmlText arguments are inserted unchanged.
func trh(lang, key string, args ...any) htmlText {
	template, ok := localeTemplate(lang, key)
	if !ok {
		return esc(key)
	}
	rendered := templateMarkup(template, true)
	if len(args) == 0 {
		return htmlText(rendered)
	}
	escaped := make([]any, len(args))
	for i, arg := range args {
		escaped[i] = escapeHTMLArg(arg)
	}
	return htmlText(fmt.Sprintf(rendered, escaped...))
}

func escapeHTMLArg(arg any) any {
	switch value := arg.(type) {
	case htmlText:
		return string(value)
	case string:
		return htmlEscaper.Replace(value)
	case error:
		return htmlEscaper.Replace(value.Error())
	case nil:
		return arg
	}
	switch reflect.ValueOf(arg).Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return arg
	}
	return htmlEscaper.Replace(fmt.Sprint(arg))
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// plainHTML converts HTML to readable plain text for delivery fallbacks.
func plainHTML(text htmlText) string {
	return html.UnescapeString(htmlTag.ReplaceAllString(string(text), ""))
}

// splitTelegramHTML cuts text into chunks of at most about limit bytes at line
// boundaries. Tags left open at a cut are closed and reopened in the next
// chunk, and a hard cut never splits a tag, an entity or a UTF-8 sequence.
func splitTelegramHTML(text htmlText, limit int) []htmlText {
	if limit < 64 {
		limit = 64
	}
	if len(text) <= limit {
		return []htmlText{text}
	}
	var pieces []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			pieces = append(pieces, current.String())
			current.Reset()
		}
	}
	for _, line := range strings.SplitAfter(string(text), "\n") {
		if current.Len() > 0 && current.Len()+len(line) > limit {
			flush()
		}
		for len(line) > limit {
			cut := safeHTMLCut(line, limit)
			pieces = append(pieces, line[:cut])
			line = line[cut:]
		}
		current.WriteString(line)
		if current.Len() >= limit {
			flush()
		}
	}
	flush()
	chunks := make([]htmlText, 0, len(pieces))
	var open []string
	for _, piece := range pieces {
		var chunk strings.Builder
		for _, tag := range open {
			chunk.WriteString(tag)
		}
		chunk.WriteString(piece)
		open = openTagsAfter(open, piece)
		for i := len(open) - 1; i >= 0; i-- {
			chunk.WriteString("</" + tagName(open[i]) + ">")
		}
		chunks = append(chunks, htmlText(chunk.String()))
	}
	return chunks
}

// safeHTMLCut returns a cut position at or below limit that is outside tags,
// entities and multi-byte characters.
func safeHTMLCut(line string, limit int) int {
	cut := limit
	if lt := strings.LastIndexByte(line[:cut], '<'); lt >= 0 && !strings.Contains(line[lt:cut], ">") {
		cut = lt
	}
	if amp := strings.LastIndexByte(line[:cut], '&'); amp >= 0 && !strings.Contains(line[amp:cut], ";") && cut-amp <= 10 {
		cut = amp
	}
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	if cut == 0 {
		return limit
	}
	return cut
}

var htmlTagToken = regexp.MustCompile(`<(/?)([a-z-]+)[^>]*>`)

// openTagsAfter updates the stack of open tags (stored as their opening
// markup, so attributes such as href survive a reopen) after piece.
func openTagsAfter(open []string, piece string) []string {
	stack := append([]string(nil), open...)
	for _, match := range htmlTagToken.FindAllStringSubmatch(piece, -1) {
		if match[1] == "/" {
			for i := len(stack) - 1; i >= 0; i-- {
				if tagName(stack[i]) == match[2] {
					stack = stack[:i]
					break
				}
			}
		} else {
			stack = append(stack, match[0])
		}
	}
	return stack
}

func tagName(openTag string) string {
	name := strings.TrimPrefix(openTag, "<")
	if end := strings.IndexAny(name, " >"); end >= 0 {
		name = name[:end]
	}
	return name
}
