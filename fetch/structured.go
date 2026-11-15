package fetch

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// StructuredLabel heads Markdown read from a page's structured data.
const StructuredLabel = "(From the page's structured data: schema.org JSON-LD that sites publish for search engines.)"

// Structured is the Markdown of the posts a page describes in its
// schema.org JSON-LD: a question with its answers (QAPage), a forum post
// with its comments, or an article's body. Q&A and forum sites publish the
// full text there for search engines, also when the page itself is an app
// shell or a login wall. It is "" when the data holds under 200 characters
// of text, as many sites give only a teaser.
func Structured(body []byte, page *url.URL) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	return structured(doc, page)
}

func structured(doc *html.Node, page *url.URL) string {
	s := &ldWriter{page: page}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.DataAtom == atom.Script && strings.EqualFold(attr(n, "type"), "application/ld+json") && n.FirstChild != nil {
			var v any
			if json.Unmarshal([]byte(n.FirstChild.Data), &v) == nil {
				s.walk(v, 0)
			}
		}
	}
	if s.text < minTrimmed {
		return ""
	}
	return StructuredLabel + "\n\n" + strings.TrimSpace(s.b.String())
}

// ldWriter writes the posts it finds; text counts their characters.
type ldWriter struct {
	b    strings.Builder
	page *url.URL
	text int
	seen int // posts written, capped
}

const maxLDDepth, maxLDPosts = 12, 500

// walk finds the post types in a JSON-LD value: a node, a list, a @graph.
func (s *ldWriter) walk(v any, depth int) {
	if depth > maxLDDepth || s.seen > maxLDPosts {
		return
	}
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			s.walk(e, depth+1)
		}
	case map[string]any:
		switch {
		case isType(t, "QAPage", "WebPage", "ItemPage"):
			s.walk(t["mainEntity"], depth+1)
			s.walk(t["@graph"], depth+1)
		case isType(t, "Question"):
			s.post(t, "# ", "text")
			s.replies("Answers", t["acceptedAnswer"], t["suggestedAnswer"])
		case isType(t, "DiscussionForumPosting", "SocialMediaPosting"):
			s.post(t, "# ", "articleBody", "text")
			s.replies("Comments", t["comment"])
		case isType(t, "Article", "NewsArticle", "BlogPosting", "TechArticle", "Report", "HowTo"):
			s.post(t, "# ", "articleBody", "text")
		default:
			s.walk(t["@graph"], depth+1)
		}
	}
}

// post writes one node's title, author and text, its first text field.
func (s *ldWriter) post(n map[string]any, heading string, fields ...string) {
	s.seen++
	if title := oneLineText(str(n["headline"]) + " " + str(n["name"])); title != "" && heading != "" {
		fmt.Fprintf(&s.b, "%s%s\n\n", heading, title)
	}
	var meta []string
	if a := author(n["author"]); a != "" {
		meta = append(meta, "by "+a)
	}
	if d := str(n["datePublished"]) + str(n["dateCreated"]); len(d) >= 10 {
		meta = append(meta, d[:10])
	}
	if votes, ok := n["upvoteCount"].(float64); ok {
		meta = append(meta, fmt.Sprintf("%d votes", int(votes)))
	}
	if len(meta) > 0 {
		s.b.WriteString(strings.Join(meta, " · ") + "\n\n")
	}
	for _, f := range fields {
		if t := s.markdown(str(n[f])); t != "" {
			s.text += len([]rune(t))
			s.b.WriteString(t + "\n\n")
			return
		}
	}
}

func (s *ldWriter) replies(heading string, lists ...any) {
	var all []map[string]any
	for _, l := range lists {
		switch t := l.(type) {
		case map[string]any:
			all = append(all, t)
		case []any:
			for _, e := range t {
				if m, ok := e.(map[string]any); ok {
					all = append(all, m)
				}
			}
		}
	}
	if len(all) == 0 {
		return
	}
	fmt.Fprintf(&s.b, "## %s\n\n", heading)
	for _, r := range all {
		if s.seen > maxLDPosts {
			return
		}
		s.b.WriteString("---\n\n")
		s.post(r, "", "text", "articleBody")
	}
}

// markdown is a text field as Markdown: sites put HTML or plain text there.
func (s *ldWriter) markdown(t string) string {
	t = strings.TrimSpace(t)
	if strings.Contains(t, "<") && strings.Contains(t, ">") {
		return htmlMarkdown([]byte(t), s.page, render{})
	}
	return t
}

func isType(n map[string]any, types ...string) bool {
	switch t := n["@type"].(type) {
	case string:
		return containsFold(types, t)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && containsFold(types, s) {
				return true
			}
		}
	}
	return false
}

func author(v any) string {
	switch t := v.(type) {
	case string:
		return oneLineText(t)
	case map[string]any:
		return oneLineText(str(t["name"]))
	case []any:
		if len(t) > 0 {
			return author(t[0])
		}
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
