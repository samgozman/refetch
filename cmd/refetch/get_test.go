package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
)

const pageURL = "https://news.example.com/notes"

const article = "<html><head><title>Notes</title></head><body><h1>Release notes</h1><p>" +
	"Shared notebooks are on every plan. Shared notebooks are on every plan. Shared notebooks are on every plan.</p></body></html>"

// fakePages stands in for the HTTP clients: it returns page and err for
// every URL.
type fakePages struct {
	page fetch.Page
	err  error
	urls []string
}

func (f *fakePages) Get(_ context.Context, url string) (fetch.Page, error) {
	f.urls = append(f.urls, url)
	return f.page, f.err
}

// okPage is pageURL as the Chrome-like client read it with HTTP 200.
func okPage(body string) fetch.Page {
	a := fetch.Attempt{URL: pageURL, Host: "news.example.com", Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeOK, Status: 200, Bytes: len(body)}
	return fetch.Page{Body: []byte(body), ContentType: "text/html", Last: a, Attempts: []fetch.Attempt{a}}
}

// usePages makes every fetcher built in this test use fp, with headless
// Chrome off so nothing outside the test runs.
func usePages(t *testing.T, fp *fakePages) {
	t.Helper()
	old := pagesFor
	pagesFor = func(config.Config) pageGetter { return fp }
	t.Cleanup(func() { pagesFor = old })
	t.Setenv("REFETCH_DATA", t.TempDir())
	t.Setenv("REFETCH_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	t.Setenv("REFETCH_RENDER", "off")
	t.Setenv("REFETCH_CHROME", filepath.Join(t.TempDir(), "no-chrome"))
}

func TestFetchPage(t *testing.T) {
	const u = pageURL
	fp := &fakePages{page: okPage(article)}
	usePages(t, fp)
	fr := newFetcherWith(config.Default(), nil, "mcp", nil)
	dir := t.TempDir()

	text, err := fetchPage(t.Context(), fr, dir, fetchInput{URL: u})
	if err != nil || !strings.HasPrefix(text, "Source: "+u+" (HTTP 200, Chrome-like client)\n") ||
		!strings.Contains(text, "untrusted page text") || !strings.Contains(text, "Release notes") {
		t.Errorf("fetch: %q, %v", text, err)
	}

	long := strings.Repeat("word ", 1000)
	fp.page = okPage("<p>" + long + "</p>")
	text, err = fetchPage(t.Context(), fr, dir, fetchInput{URL: u, MaxChars: 10})
	if err != nil || len(text) > 1300 {
		t.Errorf("max_chars below 1000 is raised to 1000: %d characters, %v", len(text), err)
	}

	text, err = fetchPage(t.Context(), fr, dir, fetchInput{URL: u, Save: true})
	if err != nil || !strings.Contains(text, "Saved the full text (4999 characters) to "+filepath.Join(dir, "pages")) {
		t.Fatalf("save: %q, %v", text, err)
	}
	path := strings.Fields(text[strings.Index(text, " to /")+4:])[0]
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), long[:100]) {
		t.Errorf("saved file %s: %v", path, err)
	}

	fp.page = fetch.Page{Last: fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeRefused, Status: 403}}
	fp.page.Attempts = []fetch.Attempt{fp.page.Last}
	fp.err = errors.New("refused (HTTP 403)")
	if _, err := fetchPage(t.Context(), fr, dir, fetchInput{URL: u}); err == nil ||
		!strings.Contains(err.Error(), "could not read "+u+": refused (HTTP 403)\nTried: Chrome-like client: refused (HTTP 403).") {
		t.Errorf("refused: %v", err)
	}
}

func TestRunGet(t *testing.T) {
	const u = pageURL
	fp := &fakePages{page: okPage(article)}
	usePages(t, fp)
	get := func(args ...string) (code int, stdout, stderr string) {
		var out, errOut bytes.Buffer
		code = run(t.Context(), append([]string{"get"}, args...), &out, &errOut)
		return code, out.String(), errOut.String()
	}

	code, out, errOut := get(u)
	if code != 0 || !strings.Contains(out, "Release notes") || strings.Contains(out, "<h1>") ||
		!strings.Contains(errOut, "chrome_like   ok") || !strings.Contains(errOut, "won: Chrome-like client") {
		t.Errorf("get: %d\n%s\n%s", code, out, errOut)
	}
	if code, out, _ := get("--raw", "--max-chars", "20", u); code != 0 || out != article[:20]+"\n[truncated]\n" {
		t.Errorf("get --raw: %d %q", code, out)
	}
	if code, _, errOut := get(); code != 1 || !strings.Contains(errOut, "get takes exactly one URL") {
		t.Errorf("no URL: %d %q", code, errOut)
	}
	if code, _, errOut := get("--impersonate", "sometimes", u); code != 1 || !strings.Contains(errOut, "impersonate") {
		t.Errorf("bad --impersonate: %d %q", code, errOut)
	}

	// A JavaScript-only page with the headless step off: the skip note.
	fp.page = fetch.Page{Last: fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeNeedsJS, Status: 200}}
	fp.page.Attempts = []fetch.Attempt{fp.page.Last}
	fp.err = errors.New("page needs JavaScript")
	if code, _, errOut := get(u); code != 1 || !strings.Contains(errOut, `render = "off"`) || !strings.Contains(errOut, "page needs JavaScript") {
		t.Errorf("needs_js: %d %q", code, errOut)
	}
}

func TestRunStats(t *testing.T) {
	const u = pageURL
	usePages(t, &fakePages{page: okPage(article)})
	stats := func(args ...string) (code int, stdout, stderr string) {
		var out, errOut bytes.Buffer
		code = run(t.Context(), append([]string{"stats"}, args...), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	var discard bytes.Buffer
	if code := run(t.Context(), []string{"get", u}, &discard, &discard); code != 0 {
		t.Fatalf("get: %d %s", code, discard.String())
	}

	if code, out, errOut := stats(); code != 0 || !strings.Contains(out, "events.db") || !strings.Contains(out, "Fetches (fetch_page and refetch get)             1") {
		t.Errorf("stats: %d\n%s\n%s", code, out, errOut)
	}
	if code, out, _ := stats("--json", "--by", "host"); code != 0 || !jsontext.Value(out).IsValid() {
		t.Errorf("stats --json: %d %q", code, out)
	}
	for _, args := range [][]string{{"--by", "path"}, {"--since", "week"}, {"--nope"}} {
		if code, _, _ := stats(args...); code == 0 {
			t.Errorf("stats %v accepted", args)
		}
	}
	if code, out, _ := stats("--reset"); code != 0 || !strings.Contains(out, "all recorded data deleted") {
		t.Errorf("stats --reset: %d %q", code, out)
	}
}
