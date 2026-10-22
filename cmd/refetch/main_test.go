package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
)

func TestMCPTool(t *testing.T) {
	t.Setenv("REFETCH_DATA", t.TempDir())
	ctx := t.Context()
	fr := newFetcher(ctx, config.Default(), "mcp", os.Stderr)
	t.Cleanup(fr.close)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := newServer(fr, config.DataDir()).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "fetch_page" || !tools.Tools[0].Annotations.ReadOnlyHint {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	props := tools.Tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)
	for _, p := range []string{"url", "max_chars", "save", "render"} {
		if props[p] == nil {
			t.Errorf("input schema lacks %s", p)
		}
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "fetch_page", Arguments: map[string]any{"url": "http://127.0.0.1:9/x"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.Contains(text, "could not read http://127.0.0.1:9/x") || !strings.Contains(text, "not a public address") {
		t.Errorf("result = %v %q", res.IsError, text)
	}
}

func TestSavePage(t *testing.T) {
	dir := t.TempDir()
	path, err := savePage(dir, "news.example.com", "full text")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "full text" || !strings.HasPrefix(path, dir+"/pages/news.example.com-") {
		t.Errorf("path %s, body %q, %v", path, b, err)
	}
}

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"7d": 7 * 24 * time.Hour, "12h": 12 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := parseSince(in); err != nil || got != want {
			t.Errorf("parseSince(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"0d", "-1h", "week", "d"} {
		if _, err := parseSince(in); err == nil {
			t.Errorf("parseSince(%q) accepted", in)
		}
	}
}

func TestRun(t *testing.T) {
	t.Setenv("REFETCH_DATA", t.TempDir())
	t.Setenv("REFETCH_CONFIG", "/nonexistent/config.toml")
	var out, errOut bytes.Buffer
	if code := run(t.Context(), []string{"stats"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "no data yet") {
		t.Errorf("stats: %d %q %q", code, out.String(), errOut.String())
	}
	if code := run(t.Context(), []string{"nope"}, &out, &errOut); code != 2 {
		t.Errorf("unknown command exit %d", code)
	}
	t.Setenv("REFETCH_MODE", "loud")
	if code := run(t.Context(), []string{"stats"}, &out, &errOut); code != 1 {
		t.Errorf("invalid config exit %d", code)
	}
}

func TestHandOff(t *testing.T) {
	cases := []struct {
		page fetch.Page
		err  error
		want []string
	}{
		{fetch.Page{Last: fetch.Attempt{Outcome: fetch.OutcomeRefused, Status: 403}, Attempts: []fetch.Attempt{{Client: fetch.ClientChromeLike, Outcome: fetch.OutcomeRefused, Status: 403}}},
			errors.New("refused"), []string{"Tried: Chrome-like client: refused (HTTP 403).", "retrying fetch_page will not help", "Claude in Chrome"}},
		{fetch.Page{Last: fetch.Attempt{Outcome: fetch.OutcomeNeedsJS, Status: 200}}, errors.New("js"), []string{"after JavaScript runs", "Playwright"}},
		{fetch.Page{Last: fetch.Attempt{Outcome: fetch.OutcomeChallenge}}, errors.New("c"), []string{"bot challenge"}},
		{fetch.Page{Last: fetch.Attempt{Outcome: fetch.OutcomeTimeout}}, errors.New("t"), []string{"did not answer in time"}},
		{fetch.Page{Last: fetch.Attempt{Outcome: fetch.OutcomeError, Status: 404}}, errors.New("HTTP 404"), []string{"does not exist (HTTP 404)"}},
		{fetch.Page{Last: fetch.Attempt{Outcome: fetch.OutcomeError, Status: 502}}, errors.New("HTTP 502"), []string{"server failed (HTTP 502)"}},
		{fetch.Page{Last: fetch.Attempt{Outcome: fetch.OutcomeError}}, errors.New("dial: 10.0.0.1 is not a public address"), []string{"public addresses only"}},
		{fetch.Page{}, errors.New(`not an http(s) URL: "ftp://x"`), []string{"http and https URLs only"}},
	}
	for _, tc := range cases {
		got := handOff(tc.page, tc.err)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("handOff(%+v) = %q, lacks %q", tc.page.Last, got, w)
			}
		}
	}
}
