package fetch

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var blankLines = regexp.MustCompile(`\n\s*\n+`)

// Text returns readable text: HTML without scripts, styles and tags;
// other types as is.
func Text(body []byte, contentType string) string {
	if !strings.Contains(contentType, "html") && !bytes.HasPrefix(bytes.TrimSpace(body), []byte("<")) {
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

// Clip cuts s to n runes, marking the cut.
func Clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "\n[truncated]"
	}
	return s
}
