package site

import (
	"os"
	"strings"
	"testing"
)

const bbArticle = "https://www.bloomberg.com/news/articles/2026-11-18/otter-banks"

func TestBloombergMatch(t *testing.T) {
	page, err := os.ReadFile("testdata/bloomberg_article.html")
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]bool{
		bbArticle: true,
		"https://bloomberg.com/opinion/articles/2026-11-18/otters":   true,
		"https://www.bloomberg.com/news/features/2026-11-17/otters":  true,
		"https://www.bloomberg.com/features/2026-otters/":            true,
		"https://WWW.Bloomberg.com/news/articles/2026-11-18/otters":  true,
		"https://www.bloomberg.co.jp/news/articles/2026-11-18/otter": false,
		"https://bloomberg.example.com/news/articles/2026-11-18/x":   false,
		"https://www.businessweek.com/news/articles/2026-11-18/x":    false,
	} {
		if got := ForPage(raw, page) != nil; got != want {
			t.Errorf("ForPage(%s) = %v", raw, got)
		}
	}
	if r := ForPage(bbArticle, page); r.Name != "Bloomberg page data" {
		t.Errorf("name %q", r.Name)
	}
}

func TestBloombergMarkdown(t *testing.T) {
	page, err := os.ReadFile("testdata/bloomberg_article.html")
	if err != nil {
		t.Fatal(err)
	}
	md, err := ForPage(bbArticle, page).Read(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"# Otter Banks Ask & Get a River Clause\n\n",
		"By Ada Otter, Ben Beaver · published 2026-11-18 08:02 UTC · updated 2026-11-18 09:30 UTC\n\n",
		"Lenders want the water to stay wet.\n\n- Banks ask for a clause.\n- The river says no.\n\n",
		"Takeaways by Bloomberg AI:\n\n- Otters negotiate.\n- Beavers build.\n\n",
		"Otter banks asked Otter Bank Inc. for a [river clause](https://www.bloomberg.com/news/articles/2026-10-01/river-clause) on Tuesday.\n\n",
		"Read the [old story](https://www.bloomberg.com/news/articles/2026-09-01/old-story), the [older one](https://www.bloomberg.com/news/articles/2026-08-01/older)",
		" and [the oldest](https://www.bloomberg.com/news/articles/2026-07-01/oldest); *Rivers Weekly* called it **wet** twice.[1]\n\n",
		"### What the river wants\n\n## What otters want\n\n",
		"- Fish more fish\n- Clean water\n\n1. Ask\n2. Wait\n\n",
		"> We like the river.\n>\n> It likes us.\n\n> A dam is a promise.\n\n",
		"Sidebar one.\n\nSidebar two.\n\nUnknown but kept.\n\nWith assistance from Cara Crane\n\n",
		"Notes:\n\n[1] *Otters hold hands* while asleep.\n\n[2] An uncited note.",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("lacks %q:\n%s", w, md)
		}
	}
	for _, no := range []string{"Advertisement", "An otter", "bbg://", "\n\n\n", "Otter Bank Inc.]", "- \n"} {
		if strings.Contains(md, no) {
			t.Errorf("has %q:\n%s", no, md)
		}
	}
}

func TestBloombergNoBody(t *testing.T) {
	story := func(s string) []byte {
		return []byte(`<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"story":` + s + `}}}</script></html>`)
	}
	for name, body := range map[string][]byte{
		"no page data": []byte(`<html><title>Bloomberg</title></html>`),
		"broken JSON":  []byte(`<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"story":{"headline":"x"</script>`),
		"no story":     []byte(`<script type="application/json" id="__NEXT_DATA__">{"props":{"pageProps":{"video":{"title":"x"}}}}</script>`),
		"null story":   story(`null`),
		"no body":      story(`{"headline":"Otters"}`),
		"empty body":   story(`{"headline":"Otters","body":{"type":"document","content":[]}}`),
		"only promos": story(`{"headline":"Otters","body":{"type":"document","content":[
			{"type":"ad","content":[{"type":"text","value":" "}]},{"type":"inline-newsletter"},
			{"type":"paragraph","content":[{"type":"text","value":"  "}]}]}}`),
		"strange body": story(`{"headline":"Otters","body":"<p>Otters</p>"}`),
	} {
		if ForPage(bbArticle, body) != nil {
			t.Errorf("%s: a route", name)
		}
	}
	md, err := ForPage(bbArticle, story(`{"headline":"Otters","byline":"Ada  Otter","publishedAt":"soon","abstract":[{"x":1}],
		"aiSummary":null,"body":{"type":"document","content":[{"type":"text","value":"Just text."}]}}`)).Read(t.Context(), nil)
	if err != nil || md != "# Otters\n\nBy Ada Otter\n\nJust text." {
		t.Errorf("%v:\n%q", err, md)
	}
}
