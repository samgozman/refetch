package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
	"time"

	"github.com/samgozman/refetch/fetch"
	"github.com/samgozman/refetch/store"
)

// The Wayback Machine step: when every live step failed, the latest
// snapshot archive.org kept with HTTP 200, read as the site served it.
const (
	archiveClient     = "wayback"
	outcomeNoSnapshot = "no_snapshot"
	archiveHost       = "web.archive.org"
	// archiveBudget caps the lookup and the snapshot together; a Get alone
	// can take two client timeouts.
	archiveBudget = 20 * time.Second
	// fastLatest keeps limit=-1 (the newest row) quick for URLs with many
	// captures.
	cdxQuery    = "https://web.archive.org/cdx/search/cdx?output=json&limit=-1&fastLatest=true&filter=statuscode:200&fl=timestamp,original&url="
	cdxTimeFmt  = "20060102150405"
	archiveDate = "2 Jan 2006"
)

var errNoSnapshot = errors.New("no snapshot")

// secretKeys are query keys that may carry a credential; a URL with one is
// never sent to archive.org. Matched whole, or as a part of the key for
// the longer words.
var (
	secretKeys     = []string{"key", "sig", "code", "auth", "sid", "pwd", "jwt", "otp"}
	secretParts    = []string{"token", "secret", "password", "passwd", "signature", "session", "sessid", "apikey", "api_key", "credential"}
	secretPrefixes = []string{"x-amz-", "x-goog-"}
)

// mayCarrySecret reports whether rawURL has userinfo or a credential-like
// query key, or a query refetch cannot parse to tell.
func mayCarrySecret(rawURL string) bool {
	u, err := neturl.Parse(rawURL)
	if err != nil || u.User != nil {
		return true
	}
	q, err := neturl.ParseQuery(u.RawQuery)
	if err != nil {
		return true
	}
	for k := range q {
		k = strings.ToLower(k)
		has := func(s string) bool { return strings.Contains(k, s) }
		prefixed := func(s string) bool { return strings.HasPrefix(k, s) }
		if slices.Contains(secretKeys, k) || slices.ContainsFunc(secretParts, has) || slices.ContainsFunc(secretPrefixes, prefixed) {
			return true
		}
	}
	return false
}

// archiveWorthy reports whether a failed read goes on to the Wayback
// Machine: not for a page the site or the settings would not let refetch
// read, a file that is not text, or a URL that may carry a secret.
func (fr *fetcher) archiveWorthy(ctx context.Context, url string, p fetch.Page, err error) bool {
	disallowed := func(a fetch.Attempt) bool { return a.Outcome == fetch.OutcomeDisallowed }
	switch {
	case !fr.archive, err == nil, len(p.Attempts) == 0, ctx.Err() != nil,
		slices.ContainsFunc(p.Attempts, disallowed),
		errors.Is(err, fetch.ErrNotText), errors.Is(err, fetch.ErrDocument),
		strings.Contains(err.Error(), "not a public address"),
		fr.cooling(archiveHost):
		return false
	}
	_, host := store.NormalizeURL(url)
	return !slices.Contains(noArchiveHosts, host) && !mayCarrySecret(url)
}

// noArchiveHosts are never looked up: archive.org itself, and Google Docs
// and Drive, whose document links work as their passwords.
var noArchiveHosts = []string{archiveHost, "archive.org", "docs.google.com", "drive.google.com"}

// viaArchive reads url's latest Wayback Machine snapshot, as one attempt
// after p's. On failure p and the live error come back with the attempt
// added, so the hand-off still explains the live failure.
func (fr *fetcher) viaArchive(ctx context.Context, url string, p fetch.Page, liveErr error) (fetch.Page, error) {
	ctx, cancel := context.WithTimeout(ctx, archiveBudget)
	defer cancel()
	target := url
	if u, err := neturl.Parse(url); err == nil {
		u.Fragment, u.RawFragment = "", ""
		target = u.String()
	}
	_, host := store.NormalizeURL(url)
	a := fetch.Attempt{URL: url, Host: host, Client: archiveClient}
	start := time.Now()
	got, ts, err := fr.snapshot(ctx, target)
	a.Duration, a.Status, a.Bytes = time.Since(start), got.Last.Status, len(got.Body)
	if err != nil {
		a.Outcome, a.Err = archiveOutcome(got, err), "Wayback Machine: "+err.Error()
		p.Attempts = append(p.Attempts, a)
		return p, liveErr
	}
	a.Outcome = fetch.OutcomeOK
	via := fmt.Sprintf("Wayback Machine's copy of %s (https://%s/web/%s/%s), not the live page, which failed: %s",
		ts.Format(archiveDate), archiveHost, ts.Format(cdxTimeFmt), target, oneLine(liveErr.Error(), 200))
	// Links resolve against the page's own address, not web.archive.org's.
	return fetch.Page{Body: got.Body, ContentType: got.ContentType, URL: target, Last: a, Attempts: append(p.Attempts, a), Via: via}, nil
}

// archiveOutcome names a failed Wayback Machine step.
func archiveOutcome(got fetch.Page, err error) string {
	switch o := got.Last.Outcome; {
	case errors.Is(err, errNoSnapshot):
		return outcomeNoSnapshot
	case isTimeoutErr(err):
		return fetch.OutcomeTimeout
	case o != "" && o != fetch.OutcomeOK:
		return o
	}
	return fetch.OutcomeError
}

// snapshot looks up url's latest snapshot with HTTP 200 and reads it raw
// (id_): no Wayback toolbar, links as the site wrote them.
func (fr *fetcher) snapshot(ctx context.Context, url string) (fetch.Page, time.Time, error) {
	got, err := fr.f.Get(ctx, cdxQuery+neturl.QueryEscape(url))
	if err != nil {
		if got.Last.Status == http.StatusTooManyRequests {
			fr.coolDown(archiveHost)
		}
		return got, time.Time{}, fmt.Errorf("lookup: %w", err)
	}
	var rows [][]string
	if json.Unmarshal(got.Body, &rows) != nil || len(rows) < 2 || len(rows[len(rows)-1]) == 0 {
		return fetch.Page{}, time.Time{}, errNoSnapshot
	}
	ts, err := time.Parse(cdxTimeFmt, rows[len(rows)-1][0])
	if err != nil {
		return fetch.Page{}, time.Time{}, errNoSnapshot
	}
	got, err = fr.f.Get(ctx, fmt.Sprintf("https://%s/web/%sid_/%s", archiveHost, ts.Format(cdxTimeFmt), url))
	if err != nil {
		if got.Last.Status == http.StatusTooManyRequests {
			fr.coolDown(archiveHost)
		}
		return got, ts, err
	}
	if strings.Contains(got.ContentType, "html") {
		switch {
		case fetch.Challenge(http.StatusOK, got.Body):
			got.Last.Outcome = fetch.OutcomeChallenge
			return got, ts, errors.New("the snapshot is a challenge page")
		case fetch.NeedsJS(got.Body, got.ContentType):
			got.Last.Outcome = fetch.OutcomeNeedsJS
			return got, ts, errors.New("the snapshot is a JavaScript-only page")
		}
	}
	return got, ts, nil
}

// archiveNote is the head line of a page read from the Wayback Machine:
// an archived copy, its date, and why the live page failed.
func archiveNote(p fetch.Page) string {
	if p.Last.Client != archiveClient {
		return ""
	}
	return "Read from the " + p.Via + ". It may be out of date."
}
