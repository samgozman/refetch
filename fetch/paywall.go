package fetch

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

// paywallChars is the most text a page cut at its paywall gives; a longer
// text with a gate's words has them in its header or footer. The New York
// Times' cut article reads about 4,400 characters.
const paywallChars = 6000

var (
	// paywallGate is a sentence only a paywall says.
	paywallGate = regexp.MustCompile(`while we (?:verify|are verifying|check|are checking) (?:your )?access|` +
		`subscribe (?:now )?to (?:continue|keep) reading|subscribe to read (?:the full|the rest|more)|` +
		`to (?:continue|keep) reading,? (?:please )?(?:subscribe|become a subscriber)|` +
		`(?:this|the) (?:article|story) is (?:only )?(?:available )?(?:for|to) (?:paying )?subscribers|` +
		`subscribers?[- ]only (?:article|story|content)`)
	alreadySubscribed = regexp.MustCompile(`already (?:a subscriber|subscribed)\?`)
	subscribePrompt   = regexp.MustCompile(`(?:subscribe|subscription) (?:now |today )?(?:to read|to continue|to unlock|for (?:full|unlimited))|` +
		`become a subscriber|want all of`)
)

// Paywall reports an HTML page whose article is for subscribers: its
// schema.org data marks the article not free, or the page gives under
// paywallChars of text (textChars, as read) and shows a paywall. A page
// marked not free can still give the whole article.
func Paywall(body []byte, contentType string, textChars int) bool {
	if !isHTML(body, contentType) {
		return false
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return false
	}
	for v := range jsonLD(doc) {
		if notFree(v, 0) {
			return true
		}
	}
	if textChars >= paywallChars {
		return false
	}
	var b strings.Builder
	shownText(doc, &b)
	shown := strings.ToLower(strings.ReplaceAll(oneLineText(b.String()), "’", "'"))
	return paywallGate.MatchString(shown) || alreadySubscribed.MatchString(shown) && subscribePrompt.MatchString(shown)
}

// notFree reports a schema.org article that isAccessibleForFree marks
// false, itself or a part of it (hasPart), as publishers mark paywalled
// text for search engines.
func notFree(v any, depth int) bool {
	if depth > maxLDDepth {
		return false
	}
	switch t := v.(type) {
	case []any:
		return slices.ContainsFunc(t, func(e any) bool { return notFree(e, depth+1) })
	case map[string]any:
		if hasType(t, isArticle) && (falseFlag(t["isAccessibleForFree"]) || partNotFree(t["hasPart"])) {
			return true
		}
		return notFree(t["@graph"], depth+1) || notFree(t["mainEntity"], depth+1)
	}
	return false
}

func isArticle(t string) bool {
	return strings.HasSuffix(strings.ToLower(t), "article") ||
		containsFold([]string{"BlogPosting", "LiveBlogPosting", "Report", "WebPage", "CreativeWork"}, t)
}

func partNotFree(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		return falseFlag(t["isAccessibleForFree"])
	case []any:
		return slices.ContainsFunc(t, func(e any) bool {
			m, ok := e.(map[string]any)
			return ok && falseFlag(m["isAccessibleForFree"])
		})
	}
	return false
}

// falseFlag is a JSON-LD false: sites write the boolean or the string.
func falseFlag(v any) bool {
	b, ok := v.(bool)
	return ok && !b || strings.EqualFold(strings.TrimSpace(str(v)), "false")
}

// shownText writes the text of n's shown elements, a space after each
// text node, so that the words of adjacent blocks stay apart.
func shownText(n *html.Node, b *strings.Builder) {
	for c := range n.ChildNodes() {
		switch {
		case c.Type == html.TextNode:
			b.WriteString(c.Data)
			b.WriteByte(' ')
		case c.Type == html.ElementNode && !hidden(c) && !skipped(c):
			shownText(c, b)
		}
	}
}
