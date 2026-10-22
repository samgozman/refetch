package fetch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectFixtures(t *testing.T) {
	cases := map[string]struct{ challenge, needsJS bool }{
		"cloudflare-challenge.html": {challenge: true},
		"datadome-captcha.html":     {challenge: true},
		"react-shell.html":          {needsJS: true},
		"next-shell.html":           {needsJS: true},
		"angular-shell.html":        {needsJS: true},
		"pow-challenge.html":        {needsJS: true},
		"humanity-check.html":       {challenge: true},
		"app-shell-login.html":      {needsJS: true},
		"small-article.html":        {},
		"tiny-page.html":            {},
	}
	for name, want := range cases {
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if got := Challenge(200, body); got != want.challenge {
			t.Errorf("%s: Challenge = %v", name, got)
		}
		if got := !Challenge(200, body) && NeedsJS(body, "text/html; charset=utf-8"); got != want.needsJS {
			t.Errorf("%s: NeedsJS = %v", name, got)
		}
	}
	if !Challenge(202, nil) {
		t.Error("an empty 202 to a GET is a challenge")
	}
	rendered := []byte(`<html><body><div id="root"><h1>Secret word: elder</h1><p>Rendered after load.</p></div><script>render()</script></body></html>`)
	if EmptyRendered(rendered) || !EmptyRendered([]byte(`<html><body><div id="root"></div><script>x()</script></body></html>`)) {
		t.Error("EmptyRendered misclassifies")
	}
	if NeedsJS([]byte(`{"id":"root"}`), "application/json") {
		t.Error("JSON is never a JavaScript shell")
	}
}

func TestNeedsJSOutcome(t *testing.T) {
	const u = "https://app.example.com/"
	shell, err := os.ReadFile(filepath.Join("testdata", "react-shell.html"))
	if err != nil {
		t.Fatal(err)
	}
	f := &Fetcher{chrome: fake(map[string]page{u: {status: 200, body: string(shell)}}, nil)}
	p, err := f.Get(t.Context(), u)
	if err == nil || p.Last.Outcome != OutcomeNeedsJS || len(p.Attempts) != 1 {
		t.Errorf("err = %v, attempts = %+v", err, p.Attempts)
	}
}
