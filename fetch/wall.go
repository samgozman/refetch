package fetch

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// wallChars is the most text a wall holds; an article runs longer.
const wallChars = 1500

// LoginForm reports a short HTML page with a visible password input.
func LoginForm(body []byte, contentType string) bool {
	doc := shortPage(body, contentType)
	return doc != nil && anyShown(doc, func(n *html.Node) bool {
		return n.DataAtom == atom.Input && strings.EqualFold(attr(n, "type"), "password")
	})
}

const goneSubject = `th(?:is|at|e) (?:page|content|post|account|profile|link|video)(?: you[^.!?|]{0,40})?`

// gonePhrase starts the text or follows a separator, never mid-sentence.
var gonePhrase = regexp.MustCompile(`(?i)(?:^|[|·•]|[:.!?…]\s|\s[-–—])\s*(?:(?:sorry|oops|hmm+|error)\W*)*(?:` +
	`404(?:\s*(?:$|[-|·•:.!?…–—]|not found|error|page))|(?:page )?not found|` +
	goneSubject + ` (?:is(?:n't| not| no longer) available|is unavailable|does(?:n't| not) exist|no longer exists|` +
	`(?:could not|couldn't|can't|cannot) be found|was(?:n't| not) found)|` +
	`(?:could not|couldn't|can't|cannot) find ` + goneSubject + `)`)

// NotFound reports a short HTML page whose title or an h1 or h2 heading
// says the page does not exist: a soft 404 served with HTTP 200.
func NotFound(body []byte, contentType string) bool {
	doc := shortPage(body, contentType)
	return doc != nil && anyShown(doc, func(n *html.Node) bool {
		return (n.DataAtom == atom.Title || n.DataAtom == atom.H1 || n.DataAtom == atom.H2) &&
			gonePhrase.MatchString(strings.ReplaceAll(oneLineText(textOf(n)), "’", "'"))
	})
}

// anyShown reports a match outside hidden and skipped subtrees: Facebook
// keeps a hidden "This page isn't available" heading in every page.
func anyShown(n *html.Node, match func(*html.Node) bool) bool {
	for c := range n.ChildNodes() {
		if c.Type == html.ElementNode && !hidden(c) && !skipped(c) && (match(c) || anyShown(c, match)) {
			return true
		}
	}
	return false
}

// shortPage parses an HTML body with under wallChars of text, or is nil.
func shortPage(body []byte, contentType string) *html.Node {
	if !isHTML(body, contentType) || utf8.RuneCountInString(Text(body, "text/html")) >= wallChars {
		return nil
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	return doc
}
