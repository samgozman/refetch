package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/samgozman/refetch/fetch"
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
	if err != nil || !strings.Contains(text, "(HTTP 200, site API)") || !strings.Contains(text, "Read through the Stack Exchange API") || !strings.Contains(text, "# apt broke") {
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
