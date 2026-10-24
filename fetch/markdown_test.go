package fetch

import (
	"net/url"
	"strings"
	"testing"
)

func TestMarkdown(t *testing.T) {
	page, _ := url.Parse("https://docs.example.com/guide/start?x=1")
	for _, tc := range []struct {
		name, html, want string
	}{
		{"headings and paragraphs", "<h1>Title</h1><p>One\n  two.</p><h3>Sub</h3><p>Three</p>", "# Title\n\nOne two.\n\n### Sub\n\nThree"},
		{"title", "<html><head><title>Docs  home</title><style>p{}</style></head><body><p>Hi</p></body></html>", "Docs home\n\nHi"},
		{"links resolve", `<p>See <a href="/api">the API</a> and <a href="../faq#q2">FAQ</a>.</p>`,
			"See [the API](https://docs.example.com/api) and [FAQ](https://docs.example.com/faq#q2)."},
		{"base href wins", `<head><base href="https://cdn.example.net/v2/"></head><a href="x.html">x</a>`, "[x](https://cdn.example.net/v2/x.html)"},
		{"unsafe and empty links keep their text", `<a href="javascript:alert(1)">run</a> <a href="data:text/html,x">data</a> <a href="#top">top</a> <a href="">none</a>`, "run data top none"},
		{"tracking parameters dropped", `<a href="https://shop.example.com/p?id=7&utm_source=x&fbclid=y">item</a>`, "[item](https://shop.example.com/p?id=7)"},
		{"bare link", `<a href="https://example.com/a">https://example.com/a</a> <a href="https://example.com/b">example.com/b</a>`, "https://example.com/a https://example.com/b"},
		{"link text escaped", `<a href="https://example.com/a(b)">[x] y</a>`, `[\[x\] y](https://example.com/a%28b%29)`},
		{"image link", `<a href="/home"><img src="logo.png" alt="Home"></a>`, "[Home](https://docs.example.com/home)"},
		{"card link", `<a href="/r/1"><div><h3>Result one</h3><p>Snippet</p></div></a>`, "[Result one Snippet](https://docs.example.com/r/1)"},
		{"lists", "<ul><li>a</li><li>b<ul><li>c</li></ul></li></ul><ol start=3><li>x</li><li><p>y</p><p>z</p></li></ol>",
			"- a\n- b\n  - c\n\n3. x\n4. y\n   z"},
		{"inline markup", "<p>A <strong>bold</strong> and <em>soft</em> <code>x := 1</code> <b> </b>end</p>", "A **bold** and _soft_ `x := 1` end"},
		{"code with backticks", "<p><code>a`b</code></p>", "``a`b``"},
		{"pre keeps whitespace", "<pre><code class=\"language-python\">def f():\n    return 1\n</code></pre>", "```python\ndef f():\n    return 1\n```"},
		{"pre with a fence inside", "<pre>```\nx\n```</pre>", "````\n```\nx\n```\n````"},
		{"blockquote", "<blockquote><p>one</p><p>two</p></blockquote>", "> one\n> \n> two"},
		{"data table", "<table><caption>Plans</caption><tr><th>Plan</th><th>Price</th></tr><tr><td>Pro | team</td><td>$10</td></tr><tr><td colspan=2>Billed yearly</td></tr></table>",
			"Plans\n\n| Plan | Price |\n| --- | --- |\n| Pro \\| team | $10 |\n| Billed yearly |  |"},
		{"layout table", "<table><tr><td><table><tr><td>nav</td></tr></table></td><td><h2>Story</h2><p>Body</p></td></tr></table>", "nav\n\n## Story\n\nBody"},
		{"one-column table", "<table><tr><td>a</td></tr><tr><td>b</td></tr></table>", "a\n\nb"},
		{"hidden and skipped", `<p>shown</p><div hidden>no</div><span aria-hidden="true">icon</span><p style="display: none">gone</p><script>x()</script><noscript>js</noscript><button>Menu</button>`, "shown"},
		{"br", "<p>line one<br>line two</p>", "line one\nline two"},
		{"links in a layout table keep their target", `<table><tr><td><a href="/x">Home</a></td></tr></table><table><tr><td><a href="/y">Docs</a><td>y<tr><td><ul><li>z</ul></table>`,
			"[Home](https://docs.example.com/x)\n\n[Docs](https://docs.example.com/y)\n\ny\n\n- z"},
		{"pre keeps blank lines", "<pre>def a():\n    pass\n\n\ndef b():\n    pass</pre>", "```\ndef a():\n    pass\n\n\ndef b():\n    pass\n```"},
		{"pre without its Copy button", "<pre><button>Copy</button><code>go test ./...</code></pre>", "```\ngo test ./...\n```"},
		{"hidden rows and cells", `<table><tr><th>a<th>b<th hidden>secret</tr><tr><td>1<td>2</tr><tr style="display:none"><td>secret<td>secret</tr></table>`,
			"| a | b |\n| --- | --- |\n| 1 | 2 |"},
		{"userinfo dropped", `<a href="https://good.example@evil.example/x">good.example</a>`, "[good.example](https://evil.example/x)"},
		{"ol start out of range", `<ol start="-9223372036854775808"><li>a</li></ol><ol start="7"><li>b</li></ol>`, "1. a\n\n7. b"},
		{"query order kept", `<a href="https://example.com/s?q=a%20b&utm_x=1&b=2">s</a>`, "[s](https://example.com/s?q=a%20b&b=2)"},
		{"custom elements are blocks", "<react-app><span><div>one</div><div>two</div></span></react-app>", "one\n\ntwo"},
		{"click-through links unwrap", `<a href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc&rut=abc">Go docs</a> <a href="https://www.google.com/url?q=https://pkg.go.dev/&sa=U">pkg</a> <a href="/search?q=https://x.example/">search</a>`,
			"[Go docs](https://go.dev/doc) [pkg](https://pkg.go.dev/) [search](https://docs.example.com/search?q=https://x.example/)"},
		{"a repeated target shows once", `<h3><a href="/r/1">Result</a></h3><a href="/r/1">docs.example.com/r/1</a><p><a href="/r/1">The <b>matched</b> words</a></p>`,
			"### [Result](https://docs.example.com/r/1)\n\ndocs.example.com/r/1\n\nThe matched words"},
		{"bold runs merge", "<p><b>Claude</b> <b>Code</b> plugins</p>", "**Claude Code** plugins"},
		{"empty rows and columns dropped", "<table><tr><td>1.</td><td></td><td>Story</td></tr><tr><td></td><td></td><td></td></tr><tr><td>2.</td><td></td><td>Other</td></tr></table>",
			"| 1. | Story |\n| --- | --- |\n| 2. | Other |"},
		{"hr", "<p>a</p><hr><p>b</p>", "a\n\n---\n\nb"},
	} {
		if got := Markdown([]byte(tc.html), "text/html", page); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestMarkdownOtherTypes(t *testing.T) {
	if got := Markdown([]byte(`{"a": 1}`), "application/json", nil); got != `{"a": 1}` {
		t.Errorf("JSON = %q", got)
	}
	if got := Markdown([]byte(`<a href="/x">x</a>`), "", nil); got != "x" {
		t.Errorf("a relative link without a page URL = %q", got)
	}
	long := "https://example.com/?q=" + strings.Repeat("a", maxLinkLen)
	if got := Markdown([]byte(`<a href="`+long+`">long</a>`), "text/html", nil); got != "long" {
		t.Errorf("a long link = %q", got)
	}
	deep := strings.Repeat("<div>", maxDepth+50) + "deep <b>text</b>" + strings.Repeat("</div>", maxDepth+50)
	if got := Markdown([]byte(deep), "text/html", nil); got != "deep text" {
		t.Errorf("deep nesting = %q", got)
	}
}

// TestMarkdownStaysLinear checks that crafted markup cannot make the output
// grow faster than the page: a wide row pads every row, and each level of
// quote or list prefixes every line.
func TestMarkdownStaysLinear(t *testing.T) {
	const size = 256 << 10
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	for name, page := range map[string]string{
		"wide row":   "<table><tr>" + strings.Repeat("<td>x", 4000) + fill("<tr><td>a<td>b"),
		"colspan":    "<table><tr><td>a<td>b" + fill("<tr><td colspan=1000>x"),
		"deep quote": strings.Repeat("<blockquote>", 150) + fill("a<br>"),
		"deep list":  strings.Repeat("<ul><li>", 150) + fill("a<br>"),
		"bare items": strings.Repeat("<li><section>", 95) + fill("a<br>"),
		"ol start":   strings.Repeat(`<ol start="999999999"><li>`, 6) + fill("a<br>"),
	} {
		if out := Markdown([]byte(page), "text/html", nil); len(out) > 4*len(page) {
			t.Errorf("%s: %d bytes of Markdown from %d bytes of HTML", name, len(out), len(page))
		}
	}
}
