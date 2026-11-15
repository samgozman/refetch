package fetch

import (
	"strings"
	"testing"
)

// discoursePage is a forum page as a browser first gets it: a header of
// links, an empty app, its data as script, and the topics in <noscript>.
func discoursePage() string {
	var topics strings.Builder
	for i := range 20 {
		topics.WriteString(`<tr><td><a href="/t/topic/` + string(rune('a'+i)) + `">Compose starts the wrong image, case ` + string(rune('a'+i)) + `</a></td><td>5</td></tr>`)
	}
	header := `<div class="d-header">` + strings.Repeat(`<a href="/x">Product link</a> `, 30) + `</div>`
	return `<html><head><title>Docker Forums</title><meta name="generator" content="Discourse 2026.11.0"></head><body>` + header +
		`<section id="main"></section><script>` + strings.Repeat("var preloaded = 1;", 8000) + `</script>` +
		`<noscript><div id="main-outlet"><h1>Latest topics</h1><table>` + topics.String() + `</table></div></noscript>` +
		`<noscript><img src="/pixel.gif"></noscript></body></html>`
}

func TestNoscriptContent(t *testing.T) {
	page := []byte(discoursePage())
	md := Markdown(page, "text/html", nil)
	if !strings.HasPrefix(md, "Docker Forums\n\n") || !strings.Contains(md, "# Latest topics") || !strings.Contains(md, "case t") || strings.Contains(md, "Product link") {
		t.Errorf("markdown:\n%.600s", md)
	}
	if text := Text(page, "text/html"); !strings.Contains(text, "Compose starts the wrong image, case a") {
		t.Errorf("text: %.300q", text)
	}
	if NeedsJS(page, "text/html") {
		t.Error("a page whose content is in <noscript> does not need JavaScript")
	}

	article := `<html><body><article><h1>Notes</h1><p>` + strings.Repeat("Shared notebooks are on every plan. ", 20) + `</p></article>` +
		`<noscript>You need to enable JavaScript to run this app.</noscript></body></html>`
	if md := Markdown([]byte(article), "text/html", nil); strings.Contains(md, "enable JavaScript") || !strings.Contains(md, "# Notes") {
		t.Errorf("a short noscript notice beside the content:\n%.300s", md)
	}
}

func TestShellDetection(t *testing.T) {
	shell := []byte(`<html><head><title>App</title></head><body><p>Loading the app, one moment.</p><script>` + strings.Repeat("window.__DATA__.push(1);", 6000) + `</script></body></html>`)
	if !NeedsJS(shell, "text/html") {
		t.Error("a large page with next to no text is a shell")
	}
	if !EmptyRendered(shell) {
		t.Error("a rendered DOM with next to no text in over 100 KB is empty")
	}
	short := []byte(`<html><body><p>Only a short note here, nothing more to read on this page.</p></body></html>`)
	if NeedsJS(short, "text/html") || EmptyRendered(short) {
		t.Error("a short page is a short page")
	}
}
