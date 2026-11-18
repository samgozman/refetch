package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
	"github.com/samgozman/refetch/stats"
	"github.com/samgozman/refetch/store"
)

const (
	cdxPrefix  = "https://web.archive.org/cdx/search/cdx?"
	snapPrefix = "https://web.archive.org/web/"
	cdxFound   = `[["timestamp","original"],["20230101000000","https://news.example.com/notes"],["20240303101010","https://news.example.com/notes"]]`
	snapshot   = `<html><body><h1>Archived notes</h1><p>` +
		`Shared notebooks were on every plan in March. See <a href="/v2">the v2 notes</a>. Shared notebooks were on every plan.</p></body></html>`
)

// archiveFake answers the live page with live and liveErr, archive.org's
// CDX lookup with cdx (an HTTP 503 when cdxStatus is set) and a snapshot
// with snap.
type archiveFake struct {
	live      fetch.Page
	liveErr   error
	cdx       string
	cdxStatus int
	snap      string
	snapType  string
	urls      []string
}

func (f *archiveFake) Get(_ context.Context, u string) (fetch.Page, error) {
	f.urls = append(f.urls, u)
	ok := func(body, ct string) fetch.Page {
		a := fetch.Attempt{URL: u, Host: "web.archive.org", Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeOK, Status: 200, Bytes: len(body)}
		return fetch.Page{Body: []byte(body), ContentType: ct, URL: u, Last: a, Attempts: []fetch.Attempt{a}}
	}
	switch {
	case strings.HasPrefix(u, cdxPrefix) && f.cdxStatus != 0:
		a := fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeError, Status: f.cdxStatus}
		if f.cdxStatus == 429 {
			a.Outcome = fetch.OutcomeRefused
		}
		return fetch.Page{Last: a, Attempts: []fetch.Attempt{a}}, fmt.Errorf("HTTP %d (chrome_like client)", f.cdxStatus)
	case strings.HasPrefix(u, cdxPrefix):
		return ok(f.cdx, "application/json"), nil
	case strings.HasPrefix(u, snapPrefix):
		return ok(f.snap, cmp.Or(f.snapType, "text/html")), nil
	}
	if f.liveErr == nil {
		return okPage(article), nil
	}
	return f.live, f.liveErr
}

func (f *archiveFake) askedArchive() bool {
	return slices.ContainsFunc(f.urls, func(u string) bool { return strings.HasPrefix(u, "https://web.archive.org/") })
}

// liveFail is pageURL failing with one attempt.
func liveFail(outcome string, status int, msg string) (fetch.Page, error) {
	a := fetch.Attempt{URL: pageURL, Host: "news.example.com", Client: fetch.ClientChromeLike, Outcome: outcome, Status: status, Err: msg}
	return fetch.Page{Last: a, Attempts: []fetch.Attempt{a}}, errors.New(msg)
}

func TestArchiveAfterFailure(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		status        int
		msg           string
	}{
		{"refused", fetch.OutcomeRefused, 403, "refused (HTTP 403, chrome_like client)"},
		{"challenge", fetch.OutcomeChallenge, 403, "challenge page (HTTP 403, chrome_like client)"},
		{"needs_js", fetch.OutcomeNeedsJS, 200, "page needs JavaScript to show its content (HTTP 200, chrome_like client)"},
		{"404", fetch.OutcomeError, 404, "HTTP 404 (chrome_like client)"},
		{"dns", fetch.OutcomeError, 0, `Get "https://news.example.com/notes": dial tcp: lookup news.example.com: no such host`},
		{"timeout", fetch.OutcomeTimeout, 0, "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fk := &archiveFake{cdx: cdxFound, snap: snapshot}
			fk.live, fk.liveErr = liveFail(tc.outcome, tc.status, tc.msg)
			fr := &fetcher{f: fk, renderOff: true, archive: true}
			text, err := fetchPage(t.Context(), fr, t.TempDir(), fetchInput{URL: pageURL + "#top"})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"Source: " + pageURL + "#top (HTTP 200, Wayback Machine)\n",
				"Read from the Wayback Machine's copy of 3 Mar 2024 (https://web.archive.org/web/20240303101010/" + pageURL +
					"), not the live page, which failed: " + oneLine(tc.msg, 200) + ". It may be out of date.\n",
				"# Archived notes",
				"[the v2 notes](https://news.example.com/v2)",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("result lacks %q:\n%s", want, text)
				}
			}
			if strings.Contains(text, "not the page as a browser shows it") {
				t.Errorf("the archived copy is labelled as a site API:\n%s", text)
			}
			wantURLs := []string{cdxPrefix + "output=json&limit=-1&fastLatest=true&filter=statuscode:200&fl=timestamp,original&url=https%3A%2F%2Fnews.example.com%2Fnotes",
				snapPrefix + "20240303101010id_/" + pageURL}
			if got := fk.urls[len(fk.urls)-2:]; !slices.Equal(got, wantURLs) {
				t.Errorf("archive.org asked for %q, want %q", got, wantURLs)
			}
		})
	}
}

func TestArchiveSkipped(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		archive   bool
		live      func() (fetch.Page, error)
	}{
		{"read ok", pageURL, true, func() (fetch.Page, error) { return fetch.Page{}, nil }},
		{"archive off", pageURL, false, func() (fetch.Page, error) { return liveFail(fetch.OutcomeRefused, 403, "refused (HTTP 403)") }},
		{"not public", pageURL, true, func() (fetch.Page, error) {
			return liveFail(fetch.OutcomeError, 0, "dial tcp 10.0.0.1:443: 10.0.0.1 is not a public address")
		}},
		{"robots", pageURL, true, func() (fetch.Page, error) {
			return liveFail(fetch.OutcomeDisallowed, 0, "disallowed by robots.txt")
		}},
		{"not text", pageURL, true, func() (fetch.Page, error) {
			p, _ := liveFail(fetch.OutcomeError, 200, "not text")
			return p, fmt.Errorf("%w (an image)", fetch.ErrNotText)
		}},
		{"document", pageURL, true, func() (fetch.Page, error) {
			p, _ := liveFail(fetch.OutcomeError, 200, "no text layer")
			return p, fetch.ErrDocument
		}},
		{"invalid", pageURL, true, func() (fetch.Page, error) { return fetch.Page{}, errors.New("bad URL") }},
		{"userinfo", "https://ada:pw@news.example.com/notes", true, func() (fetch.Page, error) { return liveFail(fetch.OutcomeRefused, 403, "refused") }},
		{"token", pageURL + "?Access_Token=abc", true, func() (fetch.Page, error) { return liveFail(fetch.OutcomeRefused, 403, "refused") }},
		{"signed S3", pageURL + "?X-Amz-Signature=abc", true, func() (fetch.Page, error) { return liveFail(fetch.OutcomeRefused, 403, "refused") }},
		{"google export", "https://docs.google.com/document/d/abc123/edit", true, func() (fetch.Page, error) { return liveFail(fetch.OutcomeRefused, 403, "refused") }},
		{"google export address", "https://docs.google.com/document/d/abc123/export?format=txt", true, func() (fetch.Page, error) {
			return liveFail(fetch.OutcomeRefused, 403, "refused")
		}},
		{"drive file", "https://drive.google.com/uc?export=download&id=abc123", true, func() (fetch.Page, error) { return liveFail(fetch.OutcomeError, 404, "HTTP 404") }},
		{"archive itself", "https://archive.org/details/notes", true, func() (fetch.Page, error) {
			return liveFail(fetch.OutcomeRefused, 403, "refused")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fk := &archiveFake{cdx: cdxFound, snap: snapshot}
			fk.live, fk.liveErr = tc.live()
			if tc.name == "read ok" {
				fk.liveErr = nil
			}
			fr := &fetcher{f: fk, renderOff: true, archive: tc.archive}
			p, err := fr.get(t.Context(), tc.url, renderAuto)
			if fk.askedArchive() || p.Last.Client == archiveClient || (err == nil) != (tc.name == "read ok") {
				t.Errorf("archive.org asked: %v, %+v, %v", fk.urls, p.Attempts, err)
			}
		})
	}
}

func TestMayCarrySecret(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.com/a?page=2&sort=new":       false,
		"https://example.com/a?q=monkey&keyword=x":    false,
		"https://example.com/a?api_key=1":             true,
		"https://example.com/a?APIKEY=1":              true,
		"https://example.com/a?sig=1&e=2":             true,
		"https://example.com/a?code=1&state=2":        true,
		"https://example.com/a?refresh_token=1":       true,
		"https://example.com/a?client_secret=1":       true,
		"https://example.com/a?PHPSESSID=1":           true,
		"https://example.com/a?X-Goog-Credential=1":   true,
		"https://example.com/a?password=1":            true,
		"https://example.com/a?auth=1":                true,
		"https://example.com/a?%zz=1":                 true,
		"https://user@example.com/a":                  true,
		"https://example.com/session-notes?page=auth": false,
	} {
		if got := mayCarrySecret(u); got != want {
			t.Errorf("mayCarrySecret(%s) = %v, want %v", u, got, want)
		}
	}
}

func TestArchiveFailed(t *testing.T) {
	for _, tc := range []struct {
		name, cdx, snap string
		cdxStatus       int
		want            string
	}{
		{"no captures", "[]", snapshot, 0, "Wayback Machine: no snapshot"},
		{"header only", `[["timestamp","original"]]`, snapshot, 0, "Wayback Machine: no snapshot"},
		{"empty body", "", snapshot, 0, "Wayback Machine: no snapshot"},
		{"bad timestamp", `[["timestamp","original"],["soon","x"]]`, snapshot, 0, "Wayback Machine: no snapshot"},
		{"lookup failed", "", snapshot, 503, "Wayback Machine: error (HTTP 503)"},
		{"challenge snapshot", cdxFound, "<html><head><title>Just a moment...</title></head><body></body></html>", 0, "Wayback Machine: challenge (HTTP 200)"},
		{"js-only snapshot", cdxFound, `<html><body><div id="root"></div><script src="/app.js"></script></body></html>`, 0, "Wayback Machine: needs_js (HTTP 200)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fk := &archiveFake{cdx: tc.cdx, cdxStatus: tc.cdxStatus, snap: tc.snap}
			fk.live, fk.liveErr = liveFail(fetch.OutcomeRefused, 403, "refused (HTTP 403, chrome_like client)")
			fr := &fetcher{f: fk, renderOff: true, archive: true}
			_, err := fetchPage(t.Context(), fr, t.TempDir(), fetchInput{URL: pageURL})
			if err == nil {
				t.Fatal("read a failed snapshot")
			}
			msg := err.Error()
			if !strings.Contains(msg, "could not read "+pageURL+": refused (HTTP 403, chrome_like client)\n") ||
				!strings.Contains(msg, "Tried: Chrome-like client: refused (HTTP 403); "+tc.want+".\n") ||
				!strings.Contains(msg, "The site refuses automated readers") {
				t.Errorf("hand-off:\n%s", msg)
			}
		})
	}
}

func TestArchiveCooldown(t *testing.T) {
	clock := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	fk := &archiveFake{cdxStatus: 429}
	fk.live, fk.liveErr = liveFail(fetch.OutcomeRefused, 403, "refused")
	fr := &fetcher{f: fk, renderOff: true, archive: true, now: func() time.Time { return clock }}
	if p, _ := fr.get(t.Context(), pageURL, renderAuto); p.Attempts[len(p.Attempts)-1].Outcome != fetch.OutcomeRefused {
		t.Fatalf("a 429 from archive.org: %+v", p.Attempts)
	}
	fk.urls = nil
	if p, _ := fr.get(t.Context(), pageURL, renderAuto); fk.askedArchive() || len(p.Attempts) != 1 {
		t.Errorf("archive.org is left alone in its cooldown: %v, %+v", fk.urls, p.Attempts)
	}
}

func TestArchiveDocumentAndStats(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fk := &archiveFake{cdx: cdxFound, snap: "# Annual report\n\nRevenue grew.", snapType: fetch.TypeMarkdown}
	fk.live, fk.liveErr = liveFail(fetch.OutcomeRefused, 403, "refused (HTTP 403, chrome_like client)")
	fr := &fetcher{f: fk, renderOff: true, archive: true, st: st, via: "mcp"}
	text, err := fetchPage(t.Context(), fr, t.TempDir(), fetchInput{URL: pageURL})
	if err != nil || !strings.Contains(text, "Revenue grew.") {
		t.Errorf("snapshot text: %v\n%s", err, text)
	}
	fk.cdx = "[]"
	if _, err := fr.get(t.Context(), pageURL, renderAuto); err == nil {
		t.Error("no snapshot read a page")
	}
	attempts, err := st.Attempts(t.Context(), time.Time{})
	if err != nil || len(attempts) != 4 || attempts[1].Client != archiveClient || attempts[1].Host != "news.example.com" || attempts[3].Outcome != outcomeNoSnapshot {
		t.Fatalf("attempts: %+v, %v", attempts, err)
	}
	r := stats.Build(nil, attempts, stats.Options{}, time.Now())
	if r.Fetches.OKBy[archiveClient] != 1 || r.Fetches.FailedBy["refused"] != 1 || r.Wayback != (stats.Wayback{Ran: 2, OK: 1, NoSnapshot: 1}) {
		t.Errorf("stats: %+v %+v", r.Fetches, r.Wayback)
	}
	var out bytes.Buffer
	r.WriteText(&out, stats.Header{})
	if !strings.Contains(out.String(), "Wayback Machine 1") || !strings.Contains(out.String(), "read 1 · no snapshot 1 · failed 0") {
		t.Errorf("report:\n%s", out.String())
	}
}

func TestRunGetArchive(t *testing.T) {
	fk := &archiveFake{cdx: cdxFound, snap: snapshot}
	fk.live, fk.liveErr = liveFail(fetch.OutcomeRefused, 403, "refused (HTTP 403, chrome_like client)")
	usePages(t, &fakePages{})
	pagesFor = func(config.Config) pageGetter { return fk }
	t.Setenv("REFETCH_ARCHIVE", "wayback")
	var out, errOut bytes.Buffer
	if code := run(t.Context(), []string{"get", pageURL}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"wayback       ok", "Read from the Wayback Machine's copy of 3 Mar 2024", "won: Wayback Machine"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut.String())
		}
	}
	if !strings.Contains(out.String(), "# Archived notes") {
		t.Errorf("stdout: %s", out.String())
	}
}
