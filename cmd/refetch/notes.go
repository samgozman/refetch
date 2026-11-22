package main

import (
	"cmp"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/samgozman/refetch/fetch"
)

const (
	loginNote = "The page asks for a login before it shows its content; refetch reads public pages only. " + browserTool
	goneNote  = "The page says it does not exist (a removed page or a wrong URL); check the URL."
	// paywallNote keeps Claude from summarising a teaser as the article.
	paywallNote = "The publisher marks this article for subscribers, so this text may stop at the paywall and be only the opening. Say so if you rely on it."
)

// socialSites show signed-out readers a partial page: an X profile has the
// bio but none of its posts.
var socialSites = []string{
	"x.com", "twitter.com", "instagram.com", "facebook.com", "threads.net", "threads.com", "tiktok.com", "linkedin.com",
}

// pageNote is the one note, if any, that tells Claude a page read well is
// not the content it was after: a soft 404, a login wall, a paywall, next
// to no text, or a social site's signed-out view. The strongest applies.
func pageNote(p fetch.Page, rawURL, text string) string {
	switch {
	case fetch.NotFound(p.Body, p.ContentType):
		return goneNote
	case fetch.LoginForm(p.Body, p.ContentType):
		return loginNote
	case fetch.Paywall(p.Body, p.ContentType, utf8.RuneCountInString(text)):
		return paywallNote
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
