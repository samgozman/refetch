package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/samgozman/refetch/fetch"
	"github.com/samgozman/refetch/render"
)

type fakeRenderer struct {
	dom   string
	err   error
	calls int
}

func (f *fakeRenderer) Render(context.Context, string) ([]byte, error) {
	f.calls++
	return []byte(f.dom), f.err
}

func TestRenderStep(t *testing.T) {
	const u = "https://app.example.com/"
	httpErr := errors.New("page needs JavaScript to show its content (HTTP 200, chrome_like client)")
	start := fetch.Page{Last: fetch.Attempt{URL: u, Host: "app.example.com", Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeNeedsJS, Status: 200}}
	start.Attempts = []fetch.Attempt{start.Last}
	rendered := "<html><body><h1>Release notes</h1><p>" + strings.Repeat("Shared notebooks are on every plan. ", 10) + "</p></body></html>"
	cases := []struct {
		name        string
		r           *fakeRenderer
		off, noCall bool
		want        string // last attempt outcome
		ok          bool
		note        string
	}{
		{"renders", &fakeRenderer{dom: rendered}, false, false, fetch.OutcomeOK, true, ""},
		{"render off", &fakeRenderer{dom: rendered}, true, false, render.OutcomeSkippedOff, false, `render = "off"`},
		{"render false", &fakeRenderer{dom: rendered}, false, true, render.OutcomeSkippedByCall, false, "render: false"},
		{"no Chrome", &fakeRenderer{err: render.ErrNoChrome}, false, false, render.OutcomeNoChrome, false, "Chrome not found"},
		{"root", &fakeRenderer{err: render.ErrRoot}, false, false, render.OutcomeSkippedRoot, false, "runs as root"},
		{"timeout", &fakeRenderer{err: render.ErrTimeout}, false, false, fetch.OutcomeTimeout, false, ""},
		{"challenge again", &fakeRenderer{dom: "<html><title>Just a moment...</title><body>challenge-platform</body></html>"}, false, false, fetch.OutcomeChallenge, false, ""},
	}
	for _, tc := range cases {
		fr := &fetcher{r: tc.r, renderOff: tc.off}
		mode := renderAuto
		if tc.noCall {
			mode = renderNever
		}
		p, err := fr.renderStep(t.Context(), u, mode, start, httpErr)
		last := p.Attempts[len(p.Attempts)-1]
		if last.Client != render.Client || last.Outcome != tc.want || (err == nil) != tc.ok {
			t.Errorf("%s: last %+v, err %v", tc.name, last, err)
		}
		if tc.ok && (!strings.Contains(string(p.Body), "Release notes") || p.Last.Client != render.Client) {
			t.Errorf("%s: page %+v", tc.name, p.Last)
		}
		if (tc.off || tc.noCall) && tc.r.calls != 0 {
			t.Errorf("%s: Chrome ran", tc.name)
		}
		if got := skipNote(p); tc.note != "" && !strings.Contains(got, tc.note) || tc.note == "" && got != "" {
			t.Errorf("%s: skip note %q", tc.name, got)
		}
		if !tc.ok {
			if h := handOff(p, err); !strings.Contains(h, "Tried: Chrome-like client: needs_js (HTTP 200); headless Chrome: ") {
				t.Errorf("%s: hand-off %q", tc.name, h)
			}
		}
	}
}

func TestRenderModes(t *testing.T) {
	const u = pageURL
	rendered := "<html><body><h1>Rendered notes</h1><p>" + strings.Repeat("Filled in by scripts. ", 10) + "</p></body></html>"
	refused := fetch.Page{Last: fetch.Attempt{URL: u, Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeRefused, Status: 403}}
	refused.Attempts = []fetch.Attempt{refused.Last}
	cases := []struct {
		name    string
		page    fetch.Page
		httpErr error
		mode    renderMode
		r       *fakeRenderer
		want    string // text in the result, or in the error
		chrome  bool   // Chrome's DOM won
		note    string // renderNote
	}{
		{"auto keeps a page the HTTP clients read", okPage(article), nil, renderAuto, &fakeRenderer{dom: rendered}, "Release notes", false, ""},
		{"always renders a page the HTTP clients read", okPage(article), nil, renderAlways, &fakeRenderer{dom: rendered}, "Rendered notes", true, ""},
		{"always renders a refused page", refused, errors.New("refused (HTTP 403)"), renderAlways, &fakeRenderer{dom: rendered}, "Rendered notes", true, ""},
		{"auto leaves a refused page", refused, errors.New("refused (HTTP 403)"), renderAuto, &fakeRenderer{dom: rendered}, "refused", false, ""},
		{"always falls back to the HTTP page", okPage(article), nil, renderAlways, &fakeRenderer{err: render.ErrTimeout}, "Release notes", false, "headless Chrome step failed: headless Chrome timed out"},
		{"always without Chrome", okPage(article), nil, renderAlways, &fakeRenderer{err: render.ErrNoChrome}, "Release notes", false, "Chrome not found"},
	}
	for _, tc := range cases {
		fr := &fetcher{f: &fakePages{page: tc.page, err: tc.httpErr}, r: tc.r}
		p, err := fr.get(t.Context(), u, tc.mode)
		got := string(p.Body)
		if err != nil {
			got = err.Error()
		}
		if !strings.Contains(got, tc.want) || (p.Last.Client == render.Client) != tc.chrome {
			t.Errorf("%s: %q, last %+v", tc.name, got, p.Last)
		}
		if note := renderNote(p); tc.note == "" && note != "" || !strings.Contains(note, tc.note) {
			t.Errorf("%s: note %q", tc.name, note)
		}
		if tc.mode == renderAuto && tc.r.calls != 0 {
			t.Errorf("%s: Chrome ran", tc.name)
		}
	}
}

func TestNotTextHint(t *testing.T) {
	p := fetch.Page{Last: fetch.Attempt{Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeError, Status: 200}}
	p.Attempts = []fetch.Attempt{p.Last}
	err := fmt.Errorf("%w (application/pdf)", fetch.ErrNotText)
	if h := handOff(p, err); !strings.Contains(h, "not PDFs, images or other files") {
		t.Errorf("hand-off %q", h)
	}
}
