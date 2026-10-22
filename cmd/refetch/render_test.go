package main

import (
	"context"
	"errors"
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
		p, err := fr.renderStep(t.Context(), u, !tc.noCall, start, httpErr)
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
