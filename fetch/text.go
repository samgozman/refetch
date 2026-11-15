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
	var shown, noscript strings.Builder
	z := html.NewTokenizer(bytes.NewReader(body))
	skip, inNoscript := 0, 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			text := tidyText(shown.String())
			if alt := tidyText(noscript.String()); len([]rune(alt)) >= minTrimmed && len([]rune(alt)) > 2*len([]rune(text)) {
				return strings.TrimSpace(text + "\n\n" + alt)
			}
			return text
		case html.StartTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "svg", "template":
				skip++
			case "noscript":
				inNoscript++
			case "p", "div", "li", "br", "h1", "h2", "h3", "h4", "tr", "section", "article":
				shown.WriteString("\n")
				noscript.WriteString("\n")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "svg", "template":
				skip = max(0, skip-1)
			case "noscript":
				inNoscript = max(0, inNoscript-1)
			}
		case html.TextToken:
			if skip > 0 {
				continue
			}
			if t := strings.Join(strings.Fields(string(z.Text())), " "); t != "" {
				if inNoscript > 0 {
					noscript.WriteString(t + " ")
				} else {
					shown.WriteString(t + " ")
				}
			}
		case html.SelfClosingTagToken, html.CommentToken, html.DoctypeToken:
		}
	}
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
