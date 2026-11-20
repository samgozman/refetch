package site

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

var nextData = regexp.MustCompile(`<script[^>]*\bid="__NEXT_DATA__"[^>]*>`)

// bloomberg reads an article from the story its page carries for its
// scripts: the HTML around it holds the byline and the first paragraphs, the
// data the whole text a reader without cookies is served. The data is in the
// page, so the route asks for nothing more. A page without a story body (a
// video, a section front, a paywall stub) has no route and comes back as is.
func bloomberg(u *url.URL, body []byte) *Route {
	if h := strings.ToLower(u.Hostname()); h != "www.bloomberg.com" && h != "bloomberg.com" {
		return nil
	}
	loc := nextData.FindIndex(body)
	if loc == nil {
		return nil
	}
	v, err := jsontext.NewDecoder(bytes.NewReader(body[loc[1]:])).ReadValue()
	var page struct {
		Props struct {
			PageProps struct {
				Story *bbStory `json:"story"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err != nil || json.Unmarshal(v, &page) != nil || page.Props.PageProps.Story == nil {
		return nil
	}
	md := page.Props.PageProps.Story.markdown(u)
	if md == "" {
		return nil
	}
	return &Route{Name: "Bloomberg page data", Read: func(context.Context, Getter) (string, error) {
		return md, nil
	}}
}

// bbStory holds the fields whose shape real pages show; the rest stay raw
// JSON, read leniently, so a change in one cannot cost the route.
type bbStory struct {
	Headline  string `json:"headline"`
	Byline    string `json:"byline"`
	Dek       string `json:"summaryText"`
	Published string `json:"publishedAt"`
	Updated   string `json:"updatedAt"`
	Authors   []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Abstract  jsontext.Value `json:"abstract"`
	AISummary struct {
		Text jsontext.Value `json:"text"`
	} `json:"aiSummary"`
	Body      bbNode `json:"body"`
	Footer    bbNode `json:"footer"`
	Footnotes bbNode `json:"footnotes"`
}

// bbNode is one node of the story's rich-text tree.
type bbNode struct {
	Type       string         `json:"type"`
	SubType    string         `json:"subType"`
	Value      string         `json:"value"`
	Attributes jsontext.Value `json:"attributes"`
	Data       jsontext.Value `json:"data"`
	Content    []bbNode       `json:"content"`
}

type bbData struct {
	Href        string `json:"href"`
	WebURL      string `json:"webUrl"`
	Destination struct {
		Web string `json:"web"`
	} `json:"destination"`
	Link struct {
		Href        string `json:"href"`
		Destination struct {
			Web string `json:"web"`
		} `json:"destination"`
	} `json:"link"`
	Level      int    `json:"level"`
	Identifier string `json:"identifier"`
}

func (n bbNode) data() bbData {
	var d bbData
	_ = json.Unmarshal(n.Data, &d)
	return d
}

// bbSkip are ads, newsletter sign-ups, related-story boxes and pictures.
var bbSkip = map[string]bool{"ad": true, "inline-newsletter": true, "inline-recirc": true, "media": true}

var bbInline = map[string]bool{"text": true, "link": true, "entity": true, "footnoteRef": true, "br": true}

func isBlock(n bbNode) bool { return !bbInline[n.Type] }

func (s *bbStory) markdown(base *url.URL) string {
	w := &bbWriter{base: base, notes: map[string]int{}}
	w.blocks(s.Body.Content)
	if strings.TrimSpace(w.b.String()) == "" {
		return ""
	}
	w.blocks(s.Footer.Content)
	w.footnotes(s.Footnotes)
	return strings.TrimSpace(s.header() + w.b.String())
}

func (s *bbStory) header() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", oneLine(s.Headline))
	var names []string
	for _, a := range s.Authors {
		if n := oneLine(a.Name); n != "" {
			names = append(names, n)
		}
	}
	var facts []string
	if by := strings.Join(names, ", "); by != "" {
		facts = append(facts, "By "+by)
	} else if by := oneLine(s.Byline); by != "" {
		facts = append(facts, "By "+by)
	}
	pub, perr := time.Parse(time.RFC3339, s.Published)
	if perr == nil {
		facts = append(facts, "published "+pub.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if up, err := time.Parse(time.RFC3339, s.Updated); err == nil && perr == nil && up.Sub(pub) >= time.Minute {
		facts = append(facts, "updated "+up.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if len(facts) > 0 {
		b.WriteString(strings.Join(facts, " · ") + "\n\n")
	}
	if dek := oneLine(s.Dek); dek != "" {
		b.WriteString(dek + "\n\n")
	}
	bullets(&b, "", s.Abstract)
	bullets(&b, "Takeaways by Bloomberg AI:\n\n", s.AISummary.Text)
	return b.String()
}

// bullets lists a JSON array of strings; anything else is left out.
func bullets(b *strings.Builder, label string, raw jsontext.Value) {
	var items []string
	if json.Unmarshal(raw, &items) != nil {
		return
	}
	var lines []string
	for _, it := range items {
		if it = oneLine(it); it != "" {
			lines = append(lines, "- "+it+"\n")
		}
	}
	if len(lines) > 0 {
		b.WriteString(label + strings.Join(lines, "") + "\n")
	}
}

type bbWriter struct {
	base  *url.URL
	b     strings.Builder
	notes map[string]int // footnote numbers, in the order the text cites them
}

func (w *bbWriter) blocks(nodes []bbNode) {
	for _, n := range nodes {
		w.block(n)
	}
}

func (w *bbWriter) block(n bbNode) {
	switch {
	case bbSkip[n.Type]:
	case n.Type == "paragraph":
		w.para("", w.inline(n.Content))
	case n.Type == "heading":
		w.para(strings.Repeat("#", min(max(n.data().Level, 2), 6))+" ", w.inline(n.Content))
	case n.Type == "list":
		w.list(n)
	case n.Type == "blockquote" || n.Type == "quote":
		w.quote(n)
	case slices.ContainsFunc(n.Content, isBlock):
		w.blocks(n.Content)
	default:
		// An unknown node keeps its text: one paragraph of it.
		w.para("", w.inline([]bbNode{n}))
	}
}

func (w *bbWriter) para(prefix, text string) {
	if t := strings.Join(strings.Fields(text), " "); t != "" {
		w.b.WriteString(prefix + t + "\n\n")
	}
}

func (w *bbWriter) list(n bbNode) {
	var lines []string
	for _, item := range n.Content {
		t := strings.Join(strings.Fields(w.flat(item)), " ")
		if t == "" {
			continue
		}
		mark := "- "
		if n.SubType == "ordered" {
			mark = fmt.Sprintf("%d. ", len(lines)+1)
		}
		lines = append(lines, mark+t+"\n")
	}
	if len(lines) > 0 {
		w.b.WriteString(strings.Join(lines, "") + "\n")
	}
}

func (w *bbWriter) quote(n bbNode) {
	sub := &bbWriter{base: w.base, notes: w.notes}
	sub.block(bbNode{Content: n.Content})
	t := strings.TrimSpace(sub.b.String())
	if t == "" {
		return
	}
	for line := range strings.SplitSeq(t, "\n") {
		w.b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
	}
	w.b.WriteString("\n")
}

// flat is a node's text on one line, its paragraphs joined by a space.
func (w *bbWriter) flat(n bbNode) string {
	if !isBlock(n) || !slices.ContainsFunc(n.Content, isBlock) {
		return w.inline([]bbNode{n})
	}
	var parts []string
	for _, c := range n.Content {
		if !bbSkip[c.Type] {
			parts = append(parts, w.flat(c))
		}
	}
	return strings.Join(parts, " ")
}

// inline joins the pieces as they are: each carries its own spaces.
func (w *bbWriter) inline(nodes []bbNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch {
		case bbSkip[n.Type]:
		case n.Type == "text":
			b.WriteString(n.styled())
		case n.Type == "link" || n.Type == "entity":
			text := w.inline(n.Content)
			if href := w.href(n.data()); href != "" {
				text = wrap(text, "[", "]("+href+")")
			}
			b.WriteString(text)
		case n.Type == "footnoteRef":
			fmt.Fprintf(&b, "[%d]", w.note(n.data().Identifier))
		case n.Type == "br":
			b.WriteString(" ")
		default:
			b.WriteString(n.Value + w.inline(n.Content))
		}
	}
	return b.String()
}

func (n bbNode) styled() string {
	var a struct {
		Emphasis bool `json:"emphasis"`
		Strong   bool `json:"strong"`
	}
	_ = json.Unmarshal(n.Attributes, &a)
	v := n.Value
	if a.Emphasis {
		v = wrap(v, "*", "*")
	}
	if a.Strong {
		v = wrap(v, "**", "**")
	}
	return v
}

// wrap puts open and end around s inside its edge spaces: "a " is "[a] ".
func wrap(s, open, end string) string {
	start := len(s) - len(strings.TrimLeftFunc(s, unicode.IsSpace))
	stop := len(strings.TrimRightFunc(s, unicode.IsSpace))
	if start >= stop {
		return s
	}
	return s[:start] + open + s[start:stop] + end + s[stop:]
}

// href is a link's web address. Links to other stories carry one beside
// their terminal address (bbg://); security and people links have none and
// stay text.
func (w *bbWriter) href(d bbData) string {
	for _, c := range []string{d.Destination.Web, d.Link.Destination.Web, d.WebURL, d.Href, d.Link.Href} {
		if c == "" {
			continue
		}
		if ref, err := url.Parse(c); err == nil {
			if abs := w.base.ResolveReference(ref); (abs.Scheme == "http" || abs.Scheme == "https") && abs.Host != "" {
				return abs.String()
			}
		}
	}
	return ""
}

func (w *bbWriter) note(id string) int {
	if n, ok := w.notes[id]; ok {
		return n
	}
	w.notes[id] = len(w.notes) + 1
	return w.notes[id]
}

// footnotes lists the notes by number, an uncited one after the cited.
func (w *bbWriter) footnotes(doc bbNode) {
	notes := map[int]string{}
	var walk func(bbNode)
	walk = func(n bbNode) {
		if n.Type != "footnote" {
			for _, c := range n.Content {
				walk(c)
			}
			return
		}
		if t := strings.Join(strings.Fields(w.flat(bbNode{Content: n.Content})), " "); t != "" {
			notes[w.note(n.data().Identifier)] = t
		}
	}
	walk(doc)
	if len(notes) == 0 {
		return
	}
	w.b.WriteString("Notes:\n\n")
	for _, num := range slices.Sorted(maps.Keys(notes)) {
		fmt.Fprintf(&w.b, "[%d] %s\n\n", num, notes[num])
	}
}
