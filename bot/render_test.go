package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestTemplateMarkupConvertsLocaleMarkers(t *testing.T) {
	cases := map[string][2]string{
		"*Users* `%s`":               {"<b>Users</b> <code>%s</code>", "Users %s"},
		"Enable allow_mutations":     {"Enable allow_mutations", "Enable allow_mutations"},
		"Tools & <settings>":         {"Tools &amp; &lt;settings&gt;", "Tools & <settings>"},
		"```\n{\"a\":1}\n```":        {"<pre>{&quot;a&quot;:1}\n</pre>", "{\"a\":1}\n"},
		"unpaired * star":            {"unpaired * star", "unpaired * star"},
		"80%% of *quota* `<used>`":   {"80%% of <b>quota</b> <code>&lt;used&gt;</code>", "80%% of quota <used>"},
		"tick ` alone and *bold*":    {"tick ` alone and <b>bold</b>", "tick ` alone and bold"},
		"*bold with `code` inside*":  {"<b>bold with `code` inside</b>", "bold with `code` inside"},
		"line\n```json\nx < y\n```!": {"line\n<pre>json\nx &lt; y\n</pre>!", "line\njson\nx < y\n!"},
	}
	for template, want := range cases {
		if got := templateMarkup(template, true); got != want[0] {
			t.Errorf("HTML %q = %q, want %q", template, got, want[0])
		}
		if got := templateMarkup(template, false); got != want[1] {
			t.Errorf("plain %q = %q, want %q", template, got, want[1])
		}
	}
}

func TestTrhEscapesArgumentsButNotTrustedHTML(t *testing.T) {
	languageBundles["en"]["test.render"] = "*%s* `%s` %d %v %s %s"
	t.Cleanup(func() { delete(languageBundles["en"], "test.render") })
	got := trh("en", "test.render", "a_b_c", "<script>&", 7, []string{"x<y"}, errors.New("bad & worse"), bold("ok"))
	want := htmlText("<b>a_b_c</b> <code>&lt;script&gt;&amp;</code> 7 [x&lt;y] bad &amp; worse <b>ok</b>")
	if got != want {
		t.Fatalf("trh = %q, want %q", got, want)
	}
	if got := trh("en", "missing.<key>"); got != "missing.&lt;key&gt;" {
		t.Fatalf("missing key = %q", got)
	}
	if plainHTML(want) != "a_b_c <script>& 7 [x<y] bad & worse ok" {
		t.Fatalf("plain fallback = %q", plainHTML(want))
	}
}

// Every locale template must render to well-formed Telegram HTML.
func TestLocaleTemplatesRenderBalancedHTML(t *testing.T) {
	tag := regexp.MustCompile(`</?([a-z]+)>`)
	for lang, bundle := range languageBundles {
		for key, template := range bundle {
			rendered := templateMarkup(template, true)
			var stack []string
			for _, match := range tag.FindAllStringSubmatch(rendered, -1) {
				if strings.HasPrefix(match[0], "</") {
					if len(stack) == 0 || stack[len(stack)-1] != match[1] {
						t.Fatalf("%s %s: unbalanced %q in %q", lang, key, match[0], rendered)
					}
					stack = stack[:len(stack)-1]
				} else {
					stack = append(stack, match[1])
				}
			}
			if len(stack) != 0 {
				t.Fatalf("%s %s: unclosed %v in %q", lang, key, stack, rendered)
			}
			if strings.ContainsAny(strings.ReplaceAll(tag.ReplaceAllString(rendered, ""), "&", ""), "<>") {
				t.Fatalf("%s %s: raw angle bracket in %q", lang, key, rendered)
			}
		}
	}
}

func TestSplitTelegramHTMLKeepsTagsAndEntitiesIntact(t *testing.T) {
	var body htmlText = "<b>Title</b>\n<pre>"
	for i := 0; i < 40; i++ {
		body += "line &amp; more &lt;text&gt; 中文\n"
	}
	body += "</pre>\n" + htmlText(strings.Repeat("x&amp;", 60)) + "\n<a href=\"https://example.com/x\">link</a>"
	chunks := splitTelegramHTML(body, 200)
	if len(chunks) < 3 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	var rebuilt strings.Builder
	for i, chunk := range chunks {
		if len(chunk) > 200+len("<pre></pre><b></b>") {
			t.Fatalf("chunk %d too long: %d", i, len(chunk))
		}
		if strings.Count(string(chunk), "<pre>") != strings.Count(string(chunk), "</pre>") {
			t.Fatalf("chunk %d leaves <pre> unbalanced: %q", i, chunk)
		}
		if strings.Contains(string(chunk), "&am\n") || strings.HasSuffix(string(chunk), "&am") || strings.HasPrefix(string(chunk), "p;") {
			t.Fatalf("chunk %d splits an entity: %q", i, chunk)
		}
		rebuilt.WriteString(plainHTML(chunk))
	}
	if rebuilt.String() != plainHTML(body) {
		t.Fatal("splitting changed the visible text")
	}
	if got := splitTelegramHTML("short", 200); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short text = %q", got)
	}
}
