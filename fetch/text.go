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
// other types as is.
func Text(body []byte, contentType string) string {
	if !isHTML(body, contentType) {
		return string(body)
	}
	var b strings.Builder
	z := html.NewTokenizer(bytes.NewReader(body))
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(blankLines.ReplaceAllString(b.String(), "\n\n"))
		case html.StartTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript", "svg", "template":
				skip++
			case "p", "div", "li", "br", "h1", "h2", "h3", "h4", "tr", "section", "article":
				b.WriteString("\n")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript", "svg", "template":
				skip = max(0, skip-1)
			}
		case html.TextToken:
			if skip == 0 {
				if t := strings.Join(strings.Fields(string(z.Text())), " "); t != "" {
					b.WriteString(t + " ")
				}
			}
		case html.SelfClosingTagToken, html.CommentToken, html.DoctypeToken:
		}
	}
}

// isHTML reports a body to read as HTML: its type says so, or it starts
// with a tag.
func isHTML(body []byte, contentType string) bool {
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
