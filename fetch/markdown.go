package fetch

import (
	"bytes"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Markdown returns an HTML page as Markdown: headings, lists, links, tables
// and code blocks kept; scripts, styles and hidden elements dropped. Links
// resolve against page (a <base href> wins); only http, https and mailto
// links keep their target. Other types come back as Text returns them.
func Markdown(body []byte, contentType string, page *url.URL) string {
	if !isHTML(body, contentType) {
		return string(body)
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return Text(body, contentType)
	}
	m := &markdown{base: baseOf(doc, page), hasBlock: map[*html.Node]bool{}}
	m.markBlocks(doc)
	return strings.TrimSpace(m.blocks(doc, 0, "\n\n"))
}

// maxLinkLen drops the target of a longer link, which costs more of
// max_chars than it is worth.
const maxLinkLen = 500

// Limits that keep the output linear in the page, whatever its markup:
// below maxDepth an element is plain text; past maxNest levels a quote or
// list adds no more prefix to every line; a table wider than
// maxTableWidth or with more than maxTableCells cells is laid out as its
// cells' text, as every row would be padded to the widest one.
const (
	maxDepth      = 200
	maxNest       = 6
	maxTableWidth = 64
	maxTableCells = 20000
)

var spaceRun = regexp.MustCompile(`\s+`)

var skipTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Svg: true, atom.Template: true,
	atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Canvas: true, atom.Select: true,
	atom.Textarea: true, atom.Button: true, atom.Img: true, atom.Picture: true, atom.Video: true, atom.Audio: true,
}

var blockTags = map[atom.Atom]bool{
	atom.Html: true, atom.Head: true, atom.Body: true, atom.Address: true, atom.Article: true, atom.Aside: true,
	atom.Blockquote: true, atom.Center: true, atom.Dd: true, atom.Details: true, atom.Dialog: true, atom.Div: true,
	atom.Dl: true, atom.Dt: true, atom.Fieldset: true, atom.Figcaption: true, atom.Figure: true, atom.Footer: true,
	atom.Form: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Header: true, atom.Hgroup: true, atom.Hr: true, atom.Legend: true, atom.Li: true, atom.Main: true,
	atom.Menu: true, atom.Nav: true, atom.Ol: true, atom.P: true, atom.Pre: true, atom.Section: true,
	atom.Summary: true, atom.Table: true, atom.Caption: true, atom.Thead: true, atom.Tbody: true,
	atom.Tfoot: true, atom.Tr: true, atom.Td: true, atom.Th: true, atom.Ul: true,
}

// trackingParams are query parameters that only identify the click.
var trackingParams = []string{"fbclid", "gclid", "dclid", "msclkid", "yclid", "igshid", "mc_cid", "mc_eid", "_ga"}

type markdown struct {
	base     *url.URL
	hasBlock map[*html.Node]bool // elements with a block inside
	inLink   int
	lastLink string // the last link target shown, so a repeat shows only its text
	quotes   int    // blockquotes open
	lists    int    // lists open
}

// markBlocks fills hasBlock and reports whether n is or holds a block.
func (m *markdown) markBlocks(n *html.Node) bool {
	inner := false
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		inner = m.markBlocks(c) || inner
	}
	if inner {
		m.hasBlock[n] = true
	}
	return inner || (n.Type == html.ElementNode && blockTags[n.DataAtom])
}

// isBlock reports an element laid out as a block: a block tag, or any other
// element with a block inside (a custom element, a span around a div), but
// never a link, whose content stays its text.
func (m *markdown) isBlock(n *html.Node) bool {
	return n.Type == html.ElementNode && !hidden(n) && !skipTags[n.DataAtom] && n.DataAtom != atom.A &&
		(blockTags[n.DataAtom] || m.hasBlock[n])
}

// blocks renders n's children, joining blocks with sep and gathering the
// inline content between them into paragraphs.
func (m *markdown) blocks(n *html.Node, depth int, sep string) string {
	var out []string
	var para strings.Builder
	flush := func() {
		if s := cleanLines(para.String()); s != "" {
			out = append(out, s)
		}
		para.Reset()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if m.isBlock(c) {
			flush()
			if s := m.block(c, depth+1); s != "" {
				out = append(out, s)
			}
			continue
		}
		para.WriteString(m.inline(c, depth+1))
	}
	flush()
	return strings.Join(out, sep)
}

func (m *markdown) block(n *html.Node, depth int) string {
	if depth > maxDepth {
		return cleanLines(spaceRun.ReplaceAllString(textOf(n), " "))
	}
	switch n.DataAtom {
	case atom.Head:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.DataAtom == atom.Title {
				return oneLineText(textOf(c))
			}
		}
		return ""
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if s := oneLineText(m.children(n, depth)); s != "" {
			return strings.Repeat("#", int(n.Data[1]-'0')) + " " + s
		}
		return ""
	case atom.Ul, atom.Ol, atom.Menu:
		return m.list(n, depth)
	case atom.Li:
		return m.item(n, depth, "- ")
	case atom.Pre:
		return pre(n)
	case atom.Blockquote:
		m.quotes++
		s := m.blocks(n, depth, "\n\n")
		m.quotes--
		if s == "" || m.quotes >= maxNest {
			return s
		}
		return "> " + strings.ReplaceAll(s, "\n", "\n> ")
	case atom.Hr:
		return "---"
	case atom.Table:
		return m.table(n, depth)
	default:
		return m.blocks(n, depth, "\n\n")
	}
}

func (m *markdown) list(n *html.Node, depth int) string {
	m.lists++
	defer func() { m.lists-- }()
	var items []string
	i := 1
	if s, err := strconv.Atoi(attr(n, "start")); err == nil {
		i = s
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || hidden(c) {
			continue
		}
		marker := "- "
		if n.DataAtom == atom.Ol {
			marker = strconv.Itoa(i) + ". "
		}
		var s string
		switch c.DataAtom {
		case atom.Li:
			s = m.item(c, depth+1, marker)
			i++
		case atom.Ul, atom.Ol: // a list straight inside a list, as some sites nest them
			s = m.list(c, depth+1)
			if m.lists < maxNest {
				s = indent("  ", s)
			}
		default:
		}
		if s != "" {
			items = append(items, s)
		}
	}
	return strings.Join(items, "\n")
}

func (m *markdown) item(n *html.Node, depth int, marker string) string {
	s := m.blocks(n, depth, "\n")
	switch {
	case s == "":
		return ""
	case m.lists > maxNest:
		return marker + s
	}
	return marker + strings.TrimPrefix(indent(strings.Repeat(" ", len(marker)), s), strings.Repeat(" ", len(marker)))
}

// table renders a data table as a pipe table. A table used for layout
// (nested tables, lists or headings in its cells, or a single row or
// column) is rendered as its cells' blocks instead.
func (m *markdown) table(n *html.Node, depth int) string {
	shown := m.lastLink
	cells, width, ok := m.cells(n, depth)
	if ok {
		cells, width = trimTable(cells, width)
	}
	if !ok || len(cells) < 2 || width < 2 {
		m.lastLink = shown // the cells are rendered again, links included
		return m.blocks(n, depth, "\n\n")
	}
	var b strings.Builder
	for c := range n.ChildNodes() {
		if c.DataAtom == atom.Caption {
			if s := oneLineText(m.children(c, depth)); s != "" {
				b.WriteString(s + "\n\n")
			}
		}
	}
	for i, row := range cells {
		for len(row) < width {
			row = append(row, "")
		}
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			b.WriteString(strings.Repeat("| --- ", width) + "|\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// cells are the table's cells as one-line Markdown, a colspan as empty
// cells after it; ok is false for a layout table.
func (m *markdown) cells(n *html.Node, depth int) (cells [][]string, width int, ok bool) {
	count := 0
	for _, tr := range tableRows(n) {
		var row []string
		for c := tr.FirstChild; c != nil; c = c.NextSibling {
			if c.DataAtom != atom.Td && c.DataAtom != atom.Th || hidden(c) {
				continue
			}
			if layoutCell(c) {
				return nil, 0, false
			}
			row = append(row, strings.ReplaceAll(oneLineText(m.blocks(c, depth+1, " ")), "|", `\|`))
			if span, err := strconv.Atoi(attr(c, "colspan")); err == nil && span > 1 {
				row = append(row, make([]string, min(span, maxTableWidth)-1)...)
			}
			if len(row) > maxTableWidth {
				return nil, 0, false
			}
		}
		width = max(width, len(row))
		if count += len(row); len(cells)*width > maxTableCells || count > maxTableCells {
			return nil, 0, false
		}
		cells = append(cells, row)
	}
	return cells, width, true
}

// trimTable drops the rows, and then the columns, that are empty
// throughout, such as spacer rows and vote-arrow columns.
func trimTable(cells [][]string, width int) ([][]string, int) {
	cells = slices.DeleteFunc(cells, func(row []string) bool {
		return !slices.ContainsFunc(row, func(c string) bool { return c != "" })
	})
	keep := make([]bool, width)
	for _, row := range cells {
		for i, c := range row {
			keep[i] = keep[i] || c != ""
		}
	}
	for i, row := range cells {
		var r []string
		for j, c := range row {
			if keep[j] {
				r = append(r, c)
			}
		}
		cells[i] = r
	}
	n := 0
	for _, k := range keep {
		if k {
			n++
		}
	}
	return cells, n
}

// tableRows are the table's own rows, not those of a table inside it.
func tableRows(n *html.Node) []*html.Node {
	var rows []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.DataAtom {
		case atom.Tr:
			if !hidden(c) {
				rows = append(rows, c)
			}
		case atom.Thead, atom.Tbody, atom.Tfoot:
			for r := c.FirstChild; r != nil; r = r.NextSibling {
				if r.DataAtom == atom.Tr && !hidden(r) {
					rows = append(rows, r)
				}
			}
		default:
		}
	}
	return rows
}

func layoutCell(n *html.Node) bool {
	for d := range n.Descendants() {
		switch d.DataAtom {
		case atom.Table, atom.Ul, atom.Ol, atom.Pre, atom.Blockquote, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Form:
			return true
		default:
		}
	}
	return false
}

func (m *markdown) children(n *html.Node, depth int) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(m.inline(c, depth+1))
	}
	return b.String()
}

func (m *markdown) inline(n *html.Node, depth int) string {
	switch {
	case n.Type == html.TextNode:
		return spaceRun.ReplaceAllString(n.Data, " ")
	case n.Type != html.ElementNode, skipTags[n.DataAtom], hidden(n):
		return ""
	case depth > maxDepth:
		return " " + spaceRun.ReplaceAllString(textOf(n), " ") + " "
	}
	switch n.DataAtom {
	case atom.Br:
		return "\n"
	case atom.A:
		return m.link(n, depth)
	case atom.Code, atom.Kbd, atom.Samp, atom.Tt:
		return code(spaceRun.ReplaceAllString(textOf(n), " "))
	case atom.Strong, atom.B, atom.Em, atom.I:
		s := m.children(n, depth)
		switch {
		case m.inLink > 0: // search engines bold every matched word in a result
			return s
		case n.DataAtom == atom.Strong || n.DataAtom == atom.B:
			return wrap(s, "**")
		}
		return wrap(s, "_")
	default:
		s := m.children(n, depth)
		if blockTags[n.DataAtom] { // a block inside inline content, such as a card inside a link
			return " " + s + " "
		}
		return s
	}
}

func (m *markdown) link(n *html.Node, depth int) string {
	m.inLink++
	s := m.children(n, depth)
	m.inLink--
	text := oneLineText(s)
	if text == "" {
		for d := range n.Descendants() {
			if d.DataAtom == atom.Img {
				text = oneLineText(attr(d, "alt"))
				break
			}
		}
	}
	if text == "" {
		return s
	}
	target := m.href(attr(n, "href"))
	lead, trail := edges(s)
	if target == "" || target == m.lastLink {
		return lead + text + trail
	}
	m.lastLink = target
	if target == text || strings.TrimPrefix(strings.TrimPrefix(target, "https://"), "http://") == text {
		return lead + target + trail
	}
	return lead + "[" + strings.NewReplacer("[", `\[`, "]", `\]`).Replace(text) + "](" + target + ")" + trail
}

// href is a link target to show, or "" for none: a same-page anchor, a
// script or data URL, or one too long to be worth its space.
func (m *markdown) href(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] == '#' {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if m.base != nil {
		u = m.base.ResolveReference(u)
	}
	if t := unwrap(u); t != nil {
		u = t
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto" {
		return ""
	}
	if u.RawQuery != "" { // filtered pair by pair, so the rest keeps its order and encoding
		pairs := strings.Split(u.RawQuery, "&")
		kept := slices.DeleteFunc(slices.Clone(pairs), func(p string) bool {
			k, _, _ := strings.Cut(p, "=")
			k, _ = url.QueryUnescape(k)
			return strings.HasPrefix(strings.ToLower(k), "utm_") || containsFold(trackingParams, k)
		})
		if len(kept) != len(pairs) {
			u.RawQuery = strings.Join(kept, "&")
		}
	}
	s := strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E").Replace(u.String())
	if len(s) > maxLinkLen {
		return ""
	}
	return s
}

// redirectPaths are where search engines and sites send a click through
// before the target, carried in one of redirectKeys.
var (
	redirectPaths = []string{"/url", "/l/", "/l.php", "/link", "/redirect", "/out", "/away"}
	redirectKeys  = []string{"q", "url", "u", "target", "to", "dest"}
)

// unwrap is the target of a click-through link (DuckDuckGo's uddg, Google's
// /url?q=), or nil.
func unwrap(u *url.URL) *url.URL {
	q := u.Query()
	keys := []string{"uddg"}
	if slices.Contains(redirectPaths, u.Path) {
		keys = append(keys, redirectKeys...)
	}
	for _, k := range keys {
		if t, err := url.Parse(q.Get(k)); err == nil && (t.Scheme == "http" || t.Scheme == "https") && t.Host != "" {
			return t
		}
	}
	return nil
}

// baseOf is the page's <base href>, resolved against the page URL, or the
// page URL itself.
func baseOf(doc *html.Node, page *url.URL) *url.URL {
	for d := range doc.Descendants() {
		if d.DataAtom == atom.Base {
			if u, err := url.Parse(attr(d, "href")); err == nil && attr(d, "href") != "" {
				if page != nil {
					return page.ResolveReference(u)
				}
				return u
			}
			break
		}
	}
	return page
}

// pre is a fenced code block, its fence longer than any backtick run in it.
func pre(n *html.Node) string {
	s := strings.TrimRight(strings.TrimPrefix(textOf(n), "\n"), " \t\n")
	if strings.TrimSpace(s) == "" {
		return ""
	}
	fence := strings.Repeat("`", max(3, longestRun(s, '`')+1))
	return fence + language(n) + "\n" + s + "\n" + fence
}

// language is a code block's language from its class, as highlighters set
// it ("language-go", "lang-go").
func language(n *html.Node) string {
	for _, e := range []*html.Node{n, n.FirstChild} {
		if e == nil || e.Type != html.ElementNode {
			continue
		}
		for c := range strings.FieldsSeq(attr(e, "class")) {
			for _, p := range []string{"language-", "lang-"} {
				if l, ok := strings.CutPrefix(c, p); ok && l != "" && !strings.ContainsAny(l, "`\n") {
					return l
				}
			}
		}
	}
	return ""
}

func code(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return s
	}
	ticks := strings.Repeat("`", longestRun(t, '`')+1)
	if strings.HasPrefix(t, "`") || strings.HasSuffix(t, "`") {
		t = " " + t + " "
	}
	lead, trail := edges(s)
	return lead + ticks + t + ticks + trail
}

func wrap(s, mark string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return s
	}
	lead, trail := edges(s)
	return lead + mark + t + mark + trail
}

// edges are the single spaces s starts and ends with, kept outside the
// markup wrapped around it.
func edges(s string) (lead, trail string) {
	if strings.HasPrefix(s, " ") {
		lead = " "
	}
	if strings.HasSuffix(s, " ") {
		trail = " "
	}
	return lead, trail
}

// textOf is n's text with its whitespace as is, without the elements the
// page does not show as text (scripts, a code block's Copy button, hidden
// ones). It walks the tree without recursion, in one pass.
func textOf(n *html.Node) string {
	var b strings.Builder
	for d := n.FirstChild; d != nil; {
		open := false
		switch {
		case d.Type == html.TextNode:
			b.WriteString(d.Data)
		case d.DataAtom == atom.Br:
			b.WriteString("\n")
		case d.Type == html.ElementNode:
			open = !skipTags[d.DataAtom] && !hidden(d)
		}
		if open && d.FirstChild != nil {
			d = d.FirstChild
			continue
		}
		for d != n && d.NextSibling == nil {
			d = d.Parent
		}
		if d == n {
			break
		}
		d = d.NextSibling
	}
	return b.String()
}

// hidden reports an element the page does not show.
func hidden(n *html.Node) bool {
	for _, a := range n.Attr {
		switch {
		case a.Key == "hidden", a.Key == "aria-hidden" && a.Val == "true":
			return true
		case a.Key == "style" && strings.Contains(strings.ReplaceAll(strings.ToLower(a.Val), " ", ""), "display:none"):
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// cleanLines collapses spaces, trims each line and drops empty ones.
func cleanLines(s string) string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.ReplaceAll(strings.Join(strings.Fields(l), " "), "** **", " "); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

func indent(prefix, s string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func longestRun(s string, r byte) int {
	best, run := 0, 0
	for i := range len(s) {
		if s[i] == r {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
