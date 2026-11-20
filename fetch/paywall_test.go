package fetch

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPaywall(t *testing.T) {
	ld := func(data string) string { return `<script type="application/ld+json">` + data + `</script>` }
	teaser := "<article><h1>Harbour cranes go electric</h1><p>The port replaced its last diesel crane this week.</p>" +
		"<p>Officials said the switch cuts noise at night.</p></article>"
	article := "<article><h1>Harbour cranes go electric</h1>" +
		strings.Repeat("<p>The port replaced its last diesel crane this week, and officials said the switch cuts noise at night.</p>", 80) + "</article>"
	gate := `<div class="gate"><p>Thank you for your patience while we verify access.</p>
		<p>Already a subscriber? <a href="/login">Log in</a>.</p><p>Want all of The Daily? <a href="/subscribe">Subscribe</a>.</p></div>`
	cases := []struct {
		name, contentType, body string
		want                    bool
	}{
		{"flat false", "text/html", ld(`{"@context":"https://schema.org","@type":"NewsArticle","headline":"H","isAccessibleForFree":false}`) + teaser, true},
		{"graph, string False", "text/html", ld(`{"@graph":[{"@type":"WebSite","name":"Daily"},{"@type":["ReportageNewsArticle"],"isAccessibleForFree":"False"}]}`) + teaser, true},
		{"top-level list", "text/html", ld(`[{"@type":"BreadcrumbList"},{"@type":"Article","isAccessibleForFree":"false"}]`) + teaser, true},
		{"hasPart", "text/html", ld(`{"@type":"NewsArticle","hasPart":{"@type":"WebPageElement","isAccessibleForFree":false,"cssSelector":".paywall"}}`) + teaser, true},
		{"hasPart list", "text/html", ld(`{"@type":"NewsArticle","isAccessibleForFree":"False","hasPart":[{"@type":"WebPageElement","isAccessibleForFree":"False","cssSelector":"[data-lock]"}]}`) + teaser, true},
		{"page's main entity", "text/html", ld(`{"@type":"WebPage","mainEntity":{"@type":"BlogPosting","isAccessibleForFree":false}}`) + teaser, true},
		{"marked but long", "text/html", ld(`{"@type":"NewsArticle","isAccessibleForFree":false}`) + article, true},
		{"free", "text/html", ld(`{"@type":"NewsArticle","isAccessibleForFree":true}`) + teaser, false},
		{"free as a string", "text/html", ld(`{"@type":"NewsArticle","isAccessibleForFree":"True"}`) + teaser, false},
		{"free part", "text/html", ld(`{"@type":"NewsArticle","hasPart":{"@type":"WebPageElement","isAccessibleForFree":true}}`) + teaser, false},
		{"false off the article", "text/html", ld(`{"@type":"NewsArticle","isAccessibleForFree":true,"isPartOf":{"@type":["CreativeWork","Product"],"isAccessibleForFree":false}}`) + teaser, false},
		{"false on a product", "text/html", ld(`{"@type":"Product","isAccessibleForFree":false}`) + teaser, false},
		{"broken JSON-LD", "text/html", ld(`{"@type":"NewsArticle","isAccessibleForFree":false`) + teaser, false},
		{"no JSON-LD", "text/html", teaser, false},
		{"rendered gate", "text/html", teaser + gate, true},
		{"continue reading", "text/html", teaser + `<p>To continue reading, please subscribe.</p>`, true},
		{"subscribers' story", "text/html", teaser + `<p>This article is only available to subscribers.</p>`, true},
		{"sign-in prompt and offer", "text/html", teaser + `<p>Already a subscriber? <a href="/l">Sign in</a></p><p>Subscribe now for full access.</p>`, true},
		{"sign-in prompt alone", "text/html", teaser + `<p>Already a subscriber? <a href="/l">Sign in</a></p>`, false},
		{"full article, header sign-in", "text/html", `<header><p>Already a subscriber? <a href="/l">Sign in</a></p><p>Subscribe now for full access.</p></header>` + article, false},
		{"hidden gate", "text/html", teaser + `<div hidden><p>Subscribe to continue reading.</p></div>` +
			`<div style="display: none"><p>Thank you for your patience while we verify access.</p></div>` +
			`<div aria-hidden="true"><p>This story is for subscribers.</p></div>`, false},
		{"access docs", "text/html", "<h1>Bucket policies</h1><p>Run the check to verify access to the bucket before checking your access keys.</p>", false},
		{"members' page", "text/html", teaser + "<p>This story is for members of the press.</p>", false},
		{"gate words in a script", "text/html", teaser + `<script>var msg = "Subscribe to continue reading";</script>`, false},
		{"footer link", "text/html", teaser + `<footer><a href="/archive">Print archives (subscribers only)</a></footer>`, false},
		{"markdown", TypeMarkdown, "# Harbour cranes\n\nSubscribe to continue reading.\n\n" + ld(`{"@type":"NewsArticle","isAccessibleForFree":false}`), false},
		{"plain text", "text/plain", "Subscribe to continue reading.", false},
	}
	for _, c := range cases {
		chars := utf8.RuneCountInString(Markdown([]byte(c.body), c.contentType, nil))
		if got := Paywall([]byte(c.body), c.contentType, chars); got != c.want {
			t.Errorf("%s: Paywall = %v, want %v", c.name, got, c.want)
		}
	}
}
