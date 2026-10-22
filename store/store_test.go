package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "data", "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string][2]string{
		"https://News.Example.com:443/a/b?utm=x#top": {"https://news.example.com/a/b", "news.example.com"},
		"http://user:pw@example.com:80":              {"http://example.com/", "example.com"},
		"https://example.com:8443/x?":                {"https://example.com:8443/x", "example.com"},
		"not a url":                                  {"not a url", ""},
	}
	for in, want := range cases {
		if u, h := NormalizeURL(in); u != want[0] || h != want[1] {
			t.Errorf("NormalizeURL(%q) = %q, %q", in, u, h)
		}
	}
}

func TestRecordCallUpserts(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	now := time.Now()
	if err := s.RecordCall(ctx, ToolCall{Time: now, SessionID: "s1", ToolUseID: "tu1", Tool: "WebFetch", URL: "https://example.com/a?q=1", Outcome: CallStarted}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCall(ctx, ToolCall{Time: now.Add(time.Second), SessionID: "s1", ToolUseID: "tu1", Tool: "WebFetch", Outcome: CallFailed, Error: strings.Repeat("x", 500)}); err != nil {
		t.Fatal(err)
	}
	calls, err := s.Calls(ctx, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Outcome != CallFailed || calls[0].URL != "https://example.com/a" || calls[0].Host != "example.com" || len(calls[0].Error) != 300 {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestAttemptsRollupReset(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	old := time.Now().Add(-200 * 24 * time.Hour)
	for _, a := range []Attempt{
		{Time: old, FetchID: "f1", Via: "mcp", URL: "https://example.com/x", Client: "chrome_like", Outcome: "ok", Status: 200, Bytes: 10, Duration: 1500 * time.Millisecond},
		{Time: old, FetchID: "f2", Via: "mcp", URL: "https://example.com/y", Client: "chrome_like", Outcome: "ok", Status: 200},
		{FetchID: "f3", Via: "cli", URL: "https://example.com/z", Client: "chrome_like", Outcome: "refused", Status: 403},
	} {
		if err := s.RecordAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.Attempts(ctx, time.Time{})
	if len(all) != 3 || all[0].Duration != 1500*time.Millisecond || all[0].Host != "example.com" {
		t.Fatalf("attempts = %+v", all)
	}
	if err := s.Rollup(ctx, time.Now().Add(-Retention)); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Attempts(ctx, time.Time{})
	if len(all) != 1 || all[0].FetchID != "f3" {
		t.Fatalf("after roll-up = %+v", all)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT n FROM daily WHERE kind = 'attempt' AND outcome = 'ok'").Scan(&n); err != nil || n != 2 {
		t.Fatalf("daily n = %d, %v", n, err)
	}
	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if all, _ = s.Attempts(ctx, time.Time{}); len(all) != 0 {
		t.Fatalf("after reset = %+v", all)
	}
}

func TestConcurrentOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			s, err := Open(t.Context(), path)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = s.Close() }()
			errs <- s.RecordAttempt(t.Context(), Attempt{FetchID: strconv.Itoa(i), Via: "mcp", URL: "https://example.com/", Client: "chrome_like", Outcome: "ok"})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestFileAndSize(t *testing.T) {
	s := open(t)
	if !strings.HasSuffix(s.File(), "events.db") {
		t.Errorf("File() = %q", s.File())
	}
	if s.Size() <= 0 {
		t.Errorf("Size() = %d for an open store", s.Size())
	}
}

func TestErrorsKeepNoQueryStrings(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	now := time.Now()
	leak := `Get "https://user:pw@example.com/p?token=SECRET1#frag": dial tcp; then http://a.example/x?api_key=SECRET2`
	if err := s.RecordCall(ctx, ToolCall{Time: now, ToolUseID: "t1", Tool: "WebFetch", URL: "https://example.com/p", Outcome: CallFailed, Error: leak}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttempt(ctx, Attempt{Time: now, FetchID: "f", URL: "https://example.com/p", Outcome: "error", Error: leak}); err != nil {
		t.Fatal(err)
	}
	calls, err := s.Calls(ctx, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := s.Attempts(ctx, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := `Get "https://example.com/p": dial tcp; then http://a.example/x`
	if len(calls) != 1 || calls[0].Error != want || len(attempts) != 1 || attempts[0].Error != want {
		t.Errorf("stored errors:\n%q\n%q\nwant %q", calls[0].Error, attempts[0].Error, want)
	}
	if fi, err := os.Stat(s.File()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("events.db mode %v, %v", fi.Mode().Perm(), err)
	}
}
