package main

import (
	"strings"
	"testing"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
)

func TestPageNote(t *testing.T) {
	const facebookLogin = `<html><head><title>NASA - National Aeronautics and Space Administration | Facebook</title></head>
		<body><h2>See more from NASA</h2><form><input name="email" placeholder="Email or phone number">
		<input type="password" name="pass" placeholder="Password"><button>Log In</button><a href="/recover">Forgot account?</a></form>
			<div aria-hidden="true"><div><h2>This page isn't available right now</h2></div></div></body></html>`
	const soft404 = `<html><head><title>Reddit - The heart of the internet</title></head>
		<body><h1>Page not found</h1><p>The page you are looking for does not exist.</p></body></html>`
	profile := "<html><head><title>NASA (@NASA) / X</title></head><body><h1>NASA</h1><p>" +
		strings.Repeat("Explore the universe and discover our home planet with the official NASA account. ", 4) + "</p></body></html>"
	cases := []struct{ name, url, body, want string }{
		{"facebook login form", "https://www.facebook.com/NASA", facebookLogin, "asks for a login"},
		{"soft 404", "https://www.reddit.com/r/golang/comments/nope/", soft404, "says it does not exist"},
		{"x profile", "https://x.com/NASA", profile, "x.com shows signed-out readers a partial page"},
		{"instagram subdomain", "https://www.instagram.com/nasa/", profile, "instagram.com shows signed-out"},
		{"mobile facebook", "https://m.facebook.com/NASA", profile, "facebook.com shows signed-out"},
		{"threads.com", "https://www.threads.com/@nasa", profile, "threads.com shows signed-out"},
		{"look-alike host", "https://www.netflix.com/title/1", profile, ""},
		{"other host", "https://news.example.com/notes", profile, ""},
		{"bad URL", "://", profile, ""},
		{"almost empty social page", "https://x.com/NASA", "<p>X</p>", "almost no text"},
	}
	for _, c := range cases {
		p := fetch.Page{Body: []byte(c.body), ContentType: "text/html", URL: c.url}
		got := pageNote(p, c.url, pageText(p, c.url, "", false))
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: pageNote = %q, want %q", c.name, got, c.want)
		}
	}
	if got := pageNote(fetch.Page{}, "https://x.com/NASA/status/1", "A whole post. "+strings.Repeat("Words ", 50)); !strings.Contains(got, "may be partial") {
		t.Errorf("the redirect-less address decides the site: %q", got)
	}

	fp := &fakePages{page: okPage(soft404)}
	usePages(t, fp)
	out, err := fetchPage(t.Context(), newFetcherWith(config.Default(), nil, "mcp", nil), t.TempDir(), fetchInput{URL: pageURL})
	if err != nil || !strings.Contains(out, goneNote) || strings.Index(out, goneNote) > strings.Index(out, "BEGIN PAGE") {
		t.Errorf("%v\n%s", err, out)
	}
}
