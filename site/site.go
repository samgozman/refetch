// Package site reads a page from its site's own public API when the page is
// a bot wall or leaves content out. Every request goes through the caller's
// getter, so through refetch's clients and their public-only dialer.
package site

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// Client is the attempt client name of a read through a site's API.
const Client = "site_api"

// Getter fetches one public URL: its body, or an error and the error answer's.
type Getter func(ctx context.Context, url string) ([]byte, error)

// ErrRateLimited marks a read refused for the API's rate limit, as HTTP 429 is.
var ErrRateLimited = errors.New("rate limited")

// Route reads one page through its site's API.
type Route struct {
	Name string // the API, as the result names it: "Stack Exchange API"
	Read func(ctx context.Context, get Getter) (markdown string, err error)
}

// ForURL is the route for a page known by its address, read instead of it.
func ForURL(raw string) *Route {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	return cmp.Or(stackExchange(u), bluesky(u), medium(u))
}

// ForPage is the route for a page its markup names, read after it.
func ForPage(raw string, body []byte) *Route {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	if r := discourse(u, body); r != nil {
		return r
	}
	if r := github(u, body); r != nil {
		return r
	}
	if r := youtube(u, body); r != nil {
		return r
	}
	return mastodon(u, body)
}

var titleTag = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func pageTitle(body []byte) string {
	head := body[:min(len(body), 64<<10)]
	if m := titleTag.FindSubmatch(head); m != nil {
		return strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
	}
	return ""
}

func generator(body []byte) string {
	head := body[:min(len(body), 64<<10)]
	i := bytes.Index(bytes.ToLower(head), []byte(`name="generator" content="`))
	if i < 0 {
		return ""
	}
	if value, _, ok := bytes.Cut(head[i+len(`name="generator" content="`):], []byte(`"`)); ok {
		return string(value)
	}
	return ""
}

// oneLine makes a name or title from a site safe for a heading.
func oneLine(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}
