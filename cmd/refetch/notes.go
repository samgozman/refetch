package main

import (
	"cmp"
	"net/url"
	"strings"

	"github.com/samgozman/refetch/fetch"
)

const (
	loginNote = "The page asks for a login before it shows its content; refetch reads public pages only. " + browserTool
	goneNote  = "The page says it does not exist (a removed page or a wrong URL); check the URL."
)

// socialSites show signed-out readers a partial page: an X profile has the
// bio but none of its posts.
var socialSites = []string{
	"x.com", "twitter.com", "instagram.com", "facebook.com", "threads.net", "threads.com", "tiktok.com", "linkedin.com",
}

// pageNote is the one note, if any, that tells Claude a page read well is
// not the content it was after: a soft 404, a login wall, next to no text,
// or a social site's signed-out view. The strongest applies.
func pageNote(p fetch.Page, rawURL, text string) string {
	switch {
	case fetch.NotFound(p.Body, p.ContentType):
		return goneNote
	case fetch.LoginForm(p.Body, p.ContentType):
		return loginNote
	}
	if note := thinNote(text); note != "" {
		return note
	}
	if site := socialSite(cmp.Or(p.URL, rawURL)); site != "" {
		return site + " shows signed-out readers a partial page, so this text may be partial: " +
			"posts, comments and full profiles can be missing and need a signed-in browser. " + browserTool
	}
	return ""
}

// socialSite is the entry of socialSites that rawURL's host is or is a
// subdomain of, or "".
func socialSite(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, s := range socialSites {
		if host == s || strings.HasSuffix(host, "."+s) {
			return s
		}
	}
	return ""
}
