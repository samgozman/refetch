package fetch

import (
	"strings"
	"testing"
	"time"
)

func TestMarkdownDropsFurniture(t *testing.T) {
	body := strings.Repeat("The valley holds forty kinds of songbird, most of them resident all year. ", 4)
	page := `<html><head><title>Birds</title></head><body>
		<a class="skip-link" href="#main">Skip to content</a>
		<ul><li><a href="#search">
			Skip <span>to</span> search</a></li></ul>
		<p><a href="#notes">Skip to the notes</a> is a link in the text? No: <a href="#n">notes below</a>.</p>
		<header><a href="/">Home</a> <a href="/shop">Shop</a></header>
		<nav><ul><li><a href="/a">Section A</a></li></ul></nav>
		<div class="Cookie-Banner">We use cookies</div>
		<div role="banner">Site banner</div>
		<main id="main">
			<article>
				<header><h1>Songbirds</h1><p>By Ada, May 2026</p></header>
				<p>` + body + `</p>
				<aside>Related: owls</aside>
				<div class="share">Share on X</div>
				<footer>Filed under birds</footer>
			</article>
		</main>
		<div class="sidebar"><h2>Popular</h2></div>
		<div id="ad">Buy now</div>
		<footer><p>Copyright</p></footer>
		<div class="add-to-cart">Add to cart</div>
		</body></html>`
	md := Markdown([]byte(page), "text/html", nil)
	for _, gone := range []string{"Skip to content", "Skip to search", "Skip to the notes", "Shop", "Section A", "cookies", "Site banner", "owls", "Share on X", "Popular", "Buy now", "Copyright"} {
		if strings.Contains(md, gone) {
			t.Errorf("kept %q:\n%s", gone, md)
		}
	}
	for _, kept := range []string{"Birds", "# Songbirds", "By Ada", "forty kinds", "Filed under birds", "Add to cart", "notes below"} {
		if !strings.Contains(md, kept) {
			t.Errorf("dropped %q:\n%s", kept, md)
		}
	}
	if full := FullMarkdown([]byte(page), "text/html", nil); !strings.Contains(full, "Section A") || !strings.Contains(full, "Copyright") {
		t.Errorf("FullMarkdown dropped furniture:\n%s", full)
	}
}

func TestMarkdownKeepsFurnitureThatHoldsTheContent(t *testing.T) {
	body := strings.Repeat("Release notes for the spring update, with every change listed. ", 4)
	page := `<body><div class="sidebar"><h2>Menu</h2></div><div class="header"><h1>Spring update</h1><p>` + body + `</p></div></body>`
	if md := Markdown([]byte(page), "text/html", nil); !strings.Contains(md, "# Spring update") || !strings.Contains(md, "every change") || strings.Contains(md, "Menu") {
		t.Errorf("content inside furniture:\n%s", md)
	}
}

func TestMarkdownFallsBackToTheWholePage(t *testing.T) {
	page := `<body><div class="menu"><p>Our menu: soup, bread, cheese.</p></div><nav><a href="/">Home</a></nav></body>`
	if md := Markdown([]byte(page), "text/html", nil); !strings.Contains(md, "soup, bread, cheese") || !strings.Contains(md, "Home") {
		t.Errorf("a page that is all furniture comes back whole:\n%s", md)
	}
}

func TestMarkdownNestedLinksStayLinear(t *testing.T) {
	page := "<body><main><h1>Map</h1><p>" + strings.Repeat("A long caption for the map below. ", 10) + "</p>" +
		"<span>" + strings.Repeat(`<svg><a href="#x">`, 200) + "deep" + "</span></main></body>"
	start := time.Now()
	if md := Markdown([]byte(page), "text/html", nil); !strings.Contains(md, "# Map") {
		t.Errorf("markdown: %.200q", md)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("nested links took %s", took)
	}
}
