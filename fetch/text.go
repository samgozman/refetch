package fetch

import (
	"bytes"
	"errors"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// ErrNotText is a page that is a file, such as a PDF or an image, rather
// than HTML or text.
var ErrNotText = errors.New("not a web page or text")

// NotText is the media type of a body that is not text, or "" for text. The
// body decides when the server named no type, or named a text type over
// PDF bytes.
func NotText(contentType string, body []byte) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = http.DetectContentType(body)
		mt, _, _ = strings.Cut(mt, ";")
	}
	if bytes.HasPrefix(body, []byte("%PDF-")) {
		return "application/pdf"
	}
	switch {
	case strings.HasPrefix(mt, "text/"), strings.HasSuffix(mt, "+xml"), strings.HasSuffix(mt, "+json"),
		mt == "application/xml", mt == "application/json", mt == "application/javascript", mt == "application/x-javascript":
		return ""
	}
	return mt
}

var blankLines = regexp.MustCompile(`\n\s*\n+`)

// Text returns readable text: HTML without scripts, styles and tags;
// other types as is. When a page's <noscript> elements, where some apps
// put the whole page for browsers without JavaScript, hold over twice the
// text it shows, their text is added.
func Text(body []byte, contentType string) string {
	if !isHTML(body, contentType) {
		return string(body)
	}
	var w textWalker
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return w.result()
		case html.StartTagToken:
			w.start(z)
		case html.EndTagToken:
			name, _ := z.TagName()
			w.end(string(name))
		case html.TextToken:
			w.text(z.Text())
		case html.SelfClosingTagToken, html.CommentToken, html.DoctypeToken:
		}
	}
}

// textWalker gathers a page's text, shown and inside <noscript>, as Text's
// tokenizer walks it.
type textWalker struct {
	shown, noscript  strings.Builder
	skip, inNoscript int
	templates        []bool // per open <template>: whether it is skipped (not a shadow root)
}

func (w *textWalker) start(z *html.Tokenizer) {
	name, hasAttr := z.TagName()
	switch string(name) {
	case "template":
		shadow := false
		for more := hasAttr; more; {
			var k []byte
			k, _, more = z.TagAttr()
			shadow = shadow || string(k) == "shadowrootmode"
		}
		w.templates = append(w.templates, !shadow)
		if !shadow {
			w.skip++
		}
	case "script", "style", "svg":
		w.skip++
	case "noscript":
		w.inNoscript++
	case "p", "div", "li", "br", "h1", "h2", "h3", "h4", "tr", "section", "article":
		w.shown.WriteString("\n")
		w.noscript.WriteString("\n")
	}
}

func (w *textWalker) end(name string) {
	switch name {
	case "template":
		if n := len(w.templates); n > 0 {
			if w.templates[n-1] {
				w.skip = max(0, w.skip-1)
			}
			w.templates = w.templates[:n-1]
		}
	case "script", "style", "svg":
		w.skip = max(0, w.skip-1)
	case "noscript":
		w.inNoscript = max(0, w.inNoscript-1)
	}
}

func (w *textWalker) text(raw []byte) {
	t := strings.Join(strings.Fields(string(raw)), " ")
	switch {
	case w.skip > 0 || t == "":
	case w.inNoscript > 0:
		w.noscript.WriteString(t + " ")
	default:
		w.shown.WriteString(t + " ")
	}
}

// result is the shown text, with the <noscript> text added when it is
// over twice as long.
func (w *textWalker) result() string {
	text := tidyText(w.shown.String())
	if alt := tidyText(w.noscript.String()); len([]rune(alt)) >= minTrimmed && len([]rune(alt)) > 2*len([]rune(text)) {
		return strings.TrimSpace(text + "\n\n" + alt)
	}
	return text
}

func tidyText(s string) string { return strings.TrimSpace(blankLines.ReplaceAllString(s, "\n\n")) }

// isHTML reports a body to read as HTML: its type says so, or it starts
// with a tag and is not Markdown, such as a document's text that opens
// with a "<".
func isHTML(body []byte, contentType string) bool {
	if strings.HasPrefix(contentType, "text/markdown") {
		return false
	}
	return strings.Contains(contentType, "html") || bytes.HasPrefix(bytes.TrimSpace(body), []byte("<"))
}

// Clip cuts s to n runes, marking the cut.
func Clip(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i] + "\n[truncated]"
		}
		count++
	}
	return s
}
