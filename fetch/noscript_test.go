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

func TestHiddenContentOfAThinPage(t *testing.T) {
	page := `<html><head><title>Agent sign-in – Help</title></head><body><main><div id="articlePage"></div>
		<div id="article-body" style="display: none;"><h2>Answer</h2><p>` + strings.Repeat("The audit log captures agent sign-in times. ", 8) + `</p></div>
		<div hidden>menu</div></main></body></html>`
	if md := Markdown([]byte(page), "text/html", nil); !strings.Contains(md, "## Answer") || !strings.Contains(md, "audit log") {
		t.Errorf("a thin page's hidden article:\n%.400s", md)
	}
	full := `<html><body><article><h1>Notes</h1><p>` + strings.Repeat("Shared notebooks are on every plan. ", 10) + `</p>` +
		`<div style="display:none">` + strings.Repeat("Hidden promo text. ", 40) + `</div></article></body></html>`
	if md := Markdown([]byte(full), "text/html", nil); strings.Contains(md, "promo") {
		t.Errorf("a page with its own content keeps hidden parts hidden:\n%.400s", md)
	}
}

func TestShadowRoots(t *testing.T) {
	page := `<html><body><article><h1>Case 0042</h1><case-view><template shadowrootmode="open"><section><h2>Resolution</h2><p>` +
		strings.Repeat("Reset the license server, then sync the seats. ", 6) + `</p><slot></slot></section></template><p>Light text.</p></case-view>` +
		`<template id="row"><p>A row template, never shown.</p></template></article></body></html>`
	md := Markdown([]byte(page), "text/html", nil)
	if !strings.Contains(md, "## Resolution") || !strings.Contains(md, "license server") || !strings.Contains(md, "Light text.") || strings.Contains(md, "row template") {
		t.Errorf("markdown:\n%s", md)
	}
	text := Text([]byte(page), "text/html")
	if !strings.Contains(text, "license server") || strings.Contains(text, "row template") {
		t.Errorf("text: %q", text)
	}
}
