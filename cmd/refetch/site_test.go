package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
	"github.com/samgozman/refetch/render"
	"github.com/samgozman/refetch/site"
)

// urlPages answers each URL by its prefix, from fails as the HTTP clients
// answer an error status, else from pages; others are a 404.
type urlPages struct {
	pages map[string]string
	fails map[string]failPage
	urls  []string
}

type failPage struct {
	status int
	body   string
}

func (f *urlPages) Get(_ context.Context, u string) (fetch.Page, error) {
	f.urls = append(f.urls, u)
	for prefix, fail := range f.fails {
		if strings.HasPrefix(u, prefix) {
			a := fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeError, Status: fail.status}
			if fail.status == 403 || fail.status == 429 {
				a.Outcome = fetch.OutcomeRefused
			}
			return fetch.Page{Last: a, Attempts: []fetch.Attempt{a}, ErrorBody: []byte(fail.body)}, fmt.Errorf("HTTP %d (chrome_like client)", fail.status)
		}
	}
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

func TestSiteCooldown(t *testing.T) {
	const q, api = "https://askubuntu.com/questions/7/apt-broke", "https://api.stackexchange.com/"
	clock := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	fp := &urlPages{
		pages: map[string]string{q: `<html><body><h1>apt broke</h1><p>` + strings.Repeat("From the page itself. ", 20) + `</p></body></html>`},
		fails: map[string]failPage{api: {status: 429}},
	}
	fr := &fetcher{f: fp, renderOff: true, now: func() time.Time { return clock }}
	apiAsked := func() int {
		return len(slices.DeleteFunc(slices.Clone(fp.urls), func(u string) bool { return !strings.HasPrefix(u, api) }))
	}
	p, err := fr.get(t.Context(), q, renderAuto)
	if err != nil || len(p.Attempts) != 2 || p.Attempts[0].Client != site.Client || p.Attempts[0].Status != 429 || apiAsked() != 1 {
		t.Fatalf("a 429 is one failed API attempt, then the page: %v, %+v", err, p.Attempts)
	}
	clock = clock.Add(59 * time.Minute)
	p, err = fr.get(t.Context(), q, renderAuto)
	if err != nil || len(p.Attempts) != 1 || p.Last.Client != fetch.ClientChromeLike || apiAsked() != 1 {
		t.Errorf("within the cooldown the API is skipped, no attempt recorded: %v, %+v", err, p.Attempts)
	}
	p, err = fr.get(t.Context(), "https://superuser.com/q/9", renderAuto)
	if err == nil || len(p.Attempts) != 1 || !strings.Contains(err.Error(), "HTTP 404") || apiAsked() != 1 {
		t.Errorf("the cooldown covers every Stack Exchange site, and a failing page fails as usual: %v, %+v", err, p.Attempts)
	}
	clock = clock.Add(2 * time.Minute)
	delete(fp.fails, api)
	if _, err := fr.get(t.Context(), q, renderAuto); err != nil || apiAsked() != 2 {
		t.Errorf("after the cooldown the API is asked again: %v, %v", err, fp.urls)
	}

	const topic, raw = "https://forum.example.com/t/printer/9", "https://forum.example.com/raw/"
	fp.pages[topic] = `<html><head><title>Printer</title><meta name="generator" content="Discourse 2026"></head><body><p>First post only.</p></body></html>`
	fp.fails[raw] = failPage{status: 429}
	if p, err = fr.get(t.Context(), topic, renderAuto); err != nil || len(p.Attempts) != 2 || p.Attempts[1].Status != 429 {
		t.Errorf("a route after the page that gets a 429: %v, %+v", err, p.Attempts)
	}
	fp.urls = nil
	p, err = fr.get(t.Context(), topic, renderAuto)
	if err != nil || len(p.Attempts) != 1 || len(fp.urls) != 1 || !strings.Contains(string(p.Body), "First post only.") {
		t.Errorf("within its cooldown the page is the answer, no attempt recorded: %v, %+v, %v", err, p.Attempts, fp.urls)
	}
}

func TestSiteRateLimitSignals(t *testing.T) {
	const ghPage, ghAPI = "https://github.com/a/b/issues/2", "https://api.github.com/"
	const sePage, seAPI = "https://superuser.com/q/42", "https://api.stackexchange.com/"
	const topic, raw = "https://forum.example.com/t/printer/9", "https://forum.example.com/raw/"
	pages := map[string]string{
		ghPage: `<html><body><div data-testid="issue-timeline-load-more"></div><p>` + strings.Repeat("Some of the thread. ", 20) + `</p></body></html>`,
		topic:  `<html><head><title>Printer</title><meta name="generator" content="Discourse 2026"></head><body><p>First post only.</p></body></html>`,
	}
	for _, tc := range []struct {
		name, page, apiHost string
		fails               map[string]failPage
		cools               bool
	}{
		{"Stack Exchange throttle", sePage, "api.stackexchange.com", map[string]failPage{seAPI: {400, `{"error_id":502,"error_message":"too many requests from this IP, more requests available in 3600 seconds","error_name":"throttle_violation"}`}}, true},
		{"Stack Exchange bad parameter", sePage, "api.stackexchange.com", map[string]failPage{seAPI: {400, `{"error_id":400,"error_message":"site is required","error_name":"bad_parameter"}`}}, false},
		{"Stack Exchange 404", sePage, "api.stackexchange.com", map[string]failPage{seAPI: {404, "not found"}}, false},
		{"GitHub rate limit", ghPage, "api.github.com", map[string]failPage{ghAPI: {403, `{"message":"API rate limit exceeded for 203.0.113.9."}`}}, true},
		{"GitHub other 403", ghPage, "api.github.com", map[string]failPage{ghAPI: {403, `{"message":"Resource not accessible"}`}}, false},
		{"any API's 429", topic, "forum.example.com", map[string]failPage{raw: {429, ""}}, true},
	} {
		fr := &fetcher{f: &urlPages{pages: pages, fails: tc.fails}, renderOff: true}
		p, _ := fr.get(t.Context(), tc.page, renderAuto)
		if !slices.ContainsFunc(p.Attempts, func(a fetch.Attempt) bool { return a.Client == site.Client && a.Outcome != fetch.OutcomeOK }) {
			t.Errorf("%s: no failed API attempt: %+v", tc.name, p.Attempts)
		}
		if got := fr.cooling(tc.apiHost); got != tc.cools {
			t.Errorf("%s: cooling %v, want %v", tc.name, got, tc.cools)
		}
		if tc.cools && (fr.cooling("forum.example.com") != (tc.apiHost == "forum.example.com") || fr.cooling("github.com")) {
			t.Errorf("%s: the cooldown covers only its API host", tc.name)
		}
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

// shellSite returns a JavaScript-only shell for page and the API answers
// for their exact URLs; others are a 404.
type shellSite struct {
	page, shell string
	api         map[string]string
	urls        []string
}

func (f *shellSite) Get(ctx context.Context, u string) (fetch.Page, error) {
	f.urls = append(f.urls, u)
	if u == f.page {
		return shellPages{shell: f.shell}.Get(ctx, u)
	}
	if body, ok := f.api[u]; ok {
		return okPage(body), nil
	}
	a := fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeError, Status: 404}
	return fetch.Page{Last: a, Attempts: []fetch.Attempt{a}}, errors.New("HTTP 404 (chrome_like client)")
}

func TestShellRoute(t *testing.T) {
	const u, api = "https://social.example/@ada/7", "https://social.example/api/v1/statuses/7"
	app := `<html><script id="initial-state" type="application/json">{}</script><div id="mastodon">`
	fp := &shellSite{page: u, shell: app + `</div></html>`, api: map[string]string{
		api:              `{"id":"7","content":"<p>Hello from the API.</p>","account":{"acct":"ada"}}`,
		api + "/context": `{"ancestors":[],"descendants":[]}`,
	}}
	rr := &fakeRenderer{dom: app + `<p>` + strings.Repeat("Hello from the rendered page. ", 10) + `</p></div></html>`}
	fr := &fetcher{f: fp, r: rr}
	p, err := fr.get(t.Context(), u, renderNever)
	if err != nil || p.Via != "Mastodon API" || !strings.Contains(string(p.Body), "Hello from the API.") || rr.calls != 0 ||
		len(p.Attempts) != 2 || p.Attempts[0].Outcome != fetch.OutcomeNeedsJS {
		t.Errorf("the shell's route, no headless Chrome: %v, %+v\n%s", err, p.Attempts, p.Body)
	}
	delete(fp.api, api+"/context")
	fp.urls = nil
	p, err = fr.get(t.Context(), u, renderAuto)
	if err != nil || p.Via != "" || !strings.Contains(string(p.Body), "Hello from the rendered page.") || rr.calls != 1 ||
		len(p.Attempts) != 3 || p.Attempts[1].Client != site.Client || p.Attempts[2].Client != render.Client || len(fp.urls) != 3 {
		t.Errorf("a failed route goes on to headless Chrome, and not again on its render: %v, %+v, %v", err, p.Attempts, fp.urls)
	}
	if _, err := fr.get(t.Context(), u, renderAlways); err != nil || rr.calls != 2 {
		t.Errorf("render: true reads the page itself: %v", err)
	}

	const video = "https://www.youtube.com/watch?v=abcdefghijk"
	fp.page, fp.shell = video, `<script>var ytInitialPlayerResponse = {"videoDetails":{"videoId":"abcdefghijk","title":"A talk","shortDescription":"All about it."}};</script>`
	out, err := fetchPage(t.Context(), fr, t.TempDir(), fetchInput{URL: video})
	if err != nil || !strings.Contains(out, "(site API)") || !strings.Contains(out, "Read from the YouTube page data") || !strings.Contains(out, "All about it.") || rr.calls != 2 {
		t.Errorf("YouTube: %v\n%s", err, out)
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
