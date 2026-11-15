package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
	"github.com/samgozman/refetch/render"
	"github.com/samgozman/refetch/site"
)

// urlPages answers each URL by its prefix; others are a 404.
type urlPages struct {
	pages map[string]string
	urls  []string
}

func (f *urlPages) Get(_ context.Context, u string) (fetch.Page, error) {
	f.urls = append(f.urls, u)
	for prefix, body := range f.pages {
		if strings.HasPrefix(u, prefix) {
			a := fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeOK, Status: 200}
			return fetch.Page{Body: []byte(body), ContentType: "text/html", URL: u, Last: a, Attempts: []fetch.Attempt{a}}, nil
		}
	}
	a := fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeError, Status: 404}
	return fetch.Page{Last: a, Attempts: []fetch.Attempt{a}}, errors.New("HTTP 404 (chrome_like client)")
}

func TestSiteRoutes(t *testing.T) {
	const q = "https://askubuntu.com/questions/7/apt-broke"
	fp := &urlPages{pages: map[string]string{
		"https://api.stackexchange.com/2.3/questions/7?": `{"items":[{"title":"apt broke","body_markdown":"Help.","score":3,"owner":{"display_name":"ada"}}]}`,
	}}
	fr := &fetcher{f: fp, renderOff: true}
	text, err := fetchPage(t.Context(), fr, t.TempDir(), fetchInput{URL: q})
	if err != nil || !strings.Contains(text, "(HTTP 200, site API)") || !strings.Contains(text, "Read from the Stack Exchange API") || !strings.Contains(text, "# apt broke") {
		t.Errorf("Stack Exchange: %v\n%s", err, text)
	}

	fp.pages = map[string]string{q: `<html><body><h1>apt broke</h1><p>` + strings.Repeat("From the page itself. ", 20) + `</p></body></html>`}
	p, err := fr.get(t.Context(), q, renderAuto)
	if err != nil || p.Last.Client != fetch.ClientChromeLike || len(p.Attempts) != 2 || p.Attempts[0].Client != site.Client || p.Attempts[0].Outcome != fetch.OutcomeError {
		t.Errorf("a failed API falls back to the page: %v, %+v", err, p.Attempts)
	}
	fp.urls = nil
	if _, err := fr.get(t.Context(), q, renderAlways); err != nil || len(fp.urls) != 1 || fp.urls[0] != q {
		t.Errorf("render: true reads the page itself: %v, %v", err, fp.urls)
	}

	const topic = "https://forum.example.com/t/printer/9"
	fp.pages = map[string]string{
		topic:                                    `<html><head><title>Printer</title><meta name="generator" content="Discourse 2026"></head><body><p>First post only.</p></body></html>`,
		"https://forum.example.com/raw/9?page=1": "ada | 2026-11-01 | #1\n\nAll the posts.",
	}
	p, err = fr.get(t.Context(), topic, renderAuto)
	if err != nil || p.Via != "Discourse /raw" || !strings.Contains(string(p.Body), "All the posts.") || len(p.Attempts) != 2 {
		t.Errorf("Discourse: %v, %+v\n%s", err, p.Attempts, p.Body)
	}
	delete(fp.pages, "https://forum.example.com/raw/9?page=1")
	if p, err = fr.get(t.Context(), topic, renderAuto); err != nil || p.Via != "" || !strings.Contains(string(p.Body), "First post only.") {
		t.Errorf("a failed /raw keeps the page: %v, %s", err, p.Body)
	}
}

// shellPages returns a JavaScript-only shell, as the HTTP clients would.
type shellPages struct{ shell string }

func (f shellPages) Get(_ context.Context, u string) (fetch.Page, error) {
	a := fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeNeedsJS, Status: 200}
	return fetch.Page{Shell: []byte(f.shell), URL: u, Last: a, Attempts: []fetch.Attempt{a}}, errors.New("page needs JavaScript to show its content")
}

func TestStructuredFallback(t *testing.T) {
	const u = "https://learn.example.com/answers/questions/5"
	text := strings.Repeat("Run the repair tool from the installer, then reboot. ", 5)
	fp := shellPages{shell: `<html><script type="application/ld+json">{"@type":"QAPage","mainEntity":{"@type":"Question","name":"Install fails","text":"It fails.",
		"acceptedAnswer":{"@type":"Answer","text":"` + text + `"}}}</script><div id="root"></div></html>`}
	fr := &fetcher{f: fp, r: &fakeRenderer{err: render.ErrTimeout}}
	out, err := fetchPage(t.Context(), fr, t.TempDir(), fetchInput{URL: u})
	if err != nil || !strings.Contains(out, "(structured data)") || !strings.Contains(out, "Read from the page's structured data") || !strings.Contains(out, "Run the repair tool") {
		t.Errorf("%v\n%s", err, out)
	}
	fr.f = shellPages{shell: `<html><div id="root"></div></html>`}
	if _, err := fetchPage(t.Context(), fr, t.TempDir(), fetchInput{URL: u}); err == nil {
		t.Error("a shell with no structured data still fails")
	}
}

func TestThinNote(t *testing.T) {
	long := strings.Repeat("A full answer with its steps and context. ", 60)
	for text, want := range map[string]string{
		"Welcome to Reddit\n\nLog in to use old Reddit. To keep Reddit safe, accounts are required.": "asks for a login",
		"Reddit - The heart of the internet": "almost no text (34 characters)",
		long:                                 "",
		long + " Please log in to comment.":  "",
	} {
		if got := thinNote(text); want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("thinNote(%.40q) = %q, want %q", text, got, want)
		}
	}
	fp := &fakePages{page: okPage("<html><body><p>Log in to continue.</p></body></html>")}
	usePages(t, fp)
	out, err := fetchPage(t.Context(), newFetcherWith(config.Default(), nil, "mcp", nil), t.TempDir(), fetchInput{URL: pageURL})
	if err != nil || !strings.Contains(out, "asks for a login") {
		t.Errorf("%v\n%s", err, out)
	}
}
