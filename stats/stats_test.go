package stats

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/samgozman/refetch/store"
)

const fp = "mcp__plugin_refetch_refetch__fetch_page"

func TestFunnel(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	calls := []store.ToolCall{
		{Time: at(0), SessionID: "s1", Tool: "WebFetch", URL: "https://a.com/1", Host: "a.com", Outcome: store.CallOK},
		{Time: at(1), SessionID: "s1", Tool: "WebFetch", URL: "https://b.com/1", Host: "b.com", Outcome: store.CallFailed},
		{Time: at(2), SessionID: "s1", Tool: fp, URL: "https://b.com/1", Host: "b.com", Outcome: store.CallOK},
		{Time: at(3), SessionID: "s1", Tool: "WebFetch", URL: "https://c.com/1", Host: "c.com", Outcome: store.CallRedirected},
		{Time: at(3), SessionID: "s1", Tool: "mcp__plugin_context-mode_context-mode__ctx_fetch_and_index", URL: "https://c.com/1", Host: "c.com", Outcome: store.CallFailed},
		{Time: at(4), SessionID: "s1", Tool: fp, URL: "https://c.com/1", Host: "c.com", Outcome: store.CallFailed},
		{Time: at(5), SessionID: "s2", Tool: "WebFetch", URL: "https://d.com/1", Host: "d.com", Outcome: store.CallFailed},
		{Time: at(30), SessionID: "s2", Tool: fp, URL: "https://d.com/1", Host: "d.com", Outcome: store.CallOK}, // too late: direct
		{Time: at(31), SessionID: "s2", Tool: "mcp__refetch__fetch_page", URL: "https://e.com/1", Host: "e.com", Outcome: store.CallOK},
	}
	attempts := []store.Attempt{
		{Time: at(2).Add(-time.Second), FetchID: "f1", URL: "https://b.com/1", Host: "b.com", Client: "chrome_like", Outcome: "error"},
		{Time: at(2).Add(-time.Second), FetchID: "f1", URL: "https://b.com/1", Host: "b.com", Client: "plain_backup", Outcome: "ok", Duration: time.Second},
		{Time: at(4).Add(-time.Second), FetchID: "f2", URL: "https://c.com/1", Host: "c.com", Client: "chrome_like", Outcome: "refused", Status: 403},
		{Time: at(30), FetchID: "f3", URL: "https://d.com/1", Host: "d.com", Client: "chrome_like", Outcome: "ok"},
	}
	r := Build(calls, attempts, Options{Since: t0, ByHost: true}, at(60))
	want := Report{Sessions: 2, Reads: 4, OKFirst: 1, Failed: 3, PickedUp: 2, Resolved: 1, StillFailed: 1, NotPickedUp: 1, Direct: 2, DirectOK: 2}
	got := Report{Sessions: r.Sessions, Reads: r.Reads, OKFirst: r.OKFirst, Failed: r.Failed, PickedUp: r.PickedUp, Resolved: r.Resolved, StillFailed: r.StillFailed, NotPickedUp: r.NotPickedUp, Direct: r.Direct, DirectOK: r.DirectOK}
	if !equal(got, want) {
		t.Errorf("funnel = %+v\nwant     %+v", got, want)
	}
	if r.ResolvedBy["plain_backup"] != 1 || r.FailedWhy["refused"] != 1 {
		t.Errorf("by = %v, why = %v", r.ResolvedBy, r.FailedWhy)
	}
	if r.Fetches.Total != 3 || r.Fetches.OK != 2 || r.Fetches.FailedBy["refused"] != 1 {
		t.Errorf("fetches = %+v", r.Fetches)
	}
	if len(r.TopHosts) != 2 || len(r.Hosts) != 4 {
		t.Errorf("top = %v, hosts = %v", r.TopHosts, r.Hosts)
	}

	var buf bytes.Buffer
	r.Hosts = nil
	r.WriteText(&buf, Header{Version: "dev", DataPath: "/tmp/events.db", Bytes: 2048})
	out := buf.String()
	for _, s := range []string{"Web reads (WebFetch + ctx_fetch_and_index)", "picked up by refetch", "plain backup", "refused 1", "Top hosts that needed refetch: b.com (1), c.com (1)", "2 KB"} {
		if !strings.Contains(out, s) {
			t.Errorf("text lacks %q:\n%s", s, out)
		}
	}

	// context-mode denied WebFetch (no post event): followed by fetch_page it
	// is a failed read picked up; followed by ctx_fetch_and_index it is not
	// counted twice; followed by nothing it is not a read.
	redirect := []store.ToolCall{
		{Time: at(0), SessionID: "s3", Tool: "WebFetch", URL: "https://g.com/1", Host: "g.com", Outcome: store.CallStarted},
		{Time: at(1), SessionID: "s3", Tool: fp, URL: "https://g.com/1", Host: "g.com", Outcome: store.CallOK},
		{Time: at(2), SessionID: "s3", Tool: "WebFetch", URL: "https://h.com/1", Host: "h.com", Outcome: store.CallStarted},
		{Time: at(2), SessionID: "s3", Tool: "mcp__plugin_context-mode_context-mode__ctx_fetch_and_index", URL: "https://h.com/1", Host: "h.com", Outcome: store.CallOK},
		{Time: at(3), SessionID: "s3", Tool: "WebFetch", URL: "https://i.com/1", Host: "i.com", Outcome: store.CallStarted},
	}
	rr := Build(redirect, nil, Options{Since: t0}, at(60))
	if rr.Reads != 2 || rr.OKFirst != 1 || rr.Failed != 1 || rr.PickedUp != 1 || rr.Resolved != 1 || rr.Direct != 0 {
		t.Errorf("redirected reads: %+v", rr)
	}

	r = Build(calls, attempts, Options{Since: t0, Session: "s2"}, at(60))
	if r.Reads != 1 || r.Direct != 2 || r.Fetches.Total != 0 {
		t.Errorf("session filter: %+v", r)
	}
}

func TestEmpty(t *testing.T) {
	var buf bytes.Buffer
	Build(nil, nil, Options{}, time.Now()).WriteText(&buf, Header{Version: "dev"})
	if !strings.Contains(buf.String(), "No web reads recorded yet") {
		t.Error(buf.String())
	}
}

func equal(a, b Report) bool {
	return a.Sessions == b.Sessions && a.Reads == b.Reads && a.OKFirst == b.OKFirst && a.Failed == b.Failed &&
		a.PickedUp == b.PickedUp && a.Resolved == b.Resolved && a.StillFailed == b.StillFailed &&
		a.NotPickedUp == b.NotPickedUp && a.Direct == b.Direct && a.DirectOK == b.DirectOK
}

func TestHeadlessStep(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	read := func(s int, u string) store.ToolCall {
		return store.ToolCall{Time: at(s), SessionID: "s", Tool: "WebFetch", URL: u, Host: "x.com", Outcome: store.CallFailed}
	}
	page := func(s int, u, outcome string) store.ToolCall {
		return store.ToolCall{Time: at(s), SessionID: "s", Tool: fp, URL: u, Host: "x.com", Outcome: outcome}
	}
	step := func(s int, id, u, client, outcome string, ms int) store.Attempt {
		return store.Attempt{Time: at(s), FetchID: id, URL: u, Host: "x.com", Client: client, Outcome: outcome, Duration: time.Duration(ms) * time.Millisecond}
	}
	calls := []store.ToolCall{
		read(0, "https://x.com/js"), page(10, "https://x.com/js", store.CallOK),
		read(20, "https://x.com/ch"), page(30, "https://x.com/ch", store.CallOK),
		read(40, "https://x.com/slow"), page(55, "https://x.com/slow", store.CallFailed),
		read(60, "https://x.com/off"), page(61, "https://x.com/off", store.CallFailed),
	}
	attempts := []store.Attempt{
		step(8, "a", "https://x.com/js", "chrome_like", "needs_js", 300), step(9, "a", "https://x.com/js", "headless", "ok", 2500),
		step(28, "b", "https://x.com/ch", "chrome_like", "challenge", 200), step(29, "b", "https://x.com/ch", "headless", "ok", 4000),
		step(54, "c", "https://x.com/slow", "chrome_like", "challenge", 200), step(54, "c", "https://x.com/slow", "headless", "timeout", 10000),
		step(61, "d", "https://x.com/off", "chrome_like", "needs_js", 200), step(61, "d", "https://x.com/off", "headless", "skipped_by_call", 0),
	}
	r := Build(calls, attempts, Options{Since: t0}, at(120))
	if r.Resolved != 2 || r.ResolvedBy["headless"] != 2 || r.HeadlessWins["needs_js"] != 1 || r.HeadlessWins["challenge"] != 1 {
		t.Errorf("resolved = %d by %v, wins %v", r.Resolved, r.ResolvedBy, r.HeadlessWins)
	}
	if r.FailedWhy["step 2 timed out"] != 1 || r.FailedWhy["step 2 skipped"] != 1 {
		t.Errorf("still failed by %v", r.FailedWhy)
	}
	want := Headless{Needed: 4, Ran: 3, OK: 2, TimedOut: 1, SkippedByCall: 1, MedianMS: 4000}
	if r.Headless != want {
		t.Errorf("headless = %+v, want %+v", r.Headless, want)
	}
	var buf bytes.Buffer
	r.WriteText(&buf, Header{Version: "dev", Chrome: "Chrome 155", RenderTimeout: "10s"})
	for _, s := range []string{"headless Chrome", "JS-only 1", "Headless Chrome step (Chrome 155, timeout 10s)", "ok 2 · timed out 1 · failed 0 · median 4.0 s", "by call 1 · off 0 · no Chrome 0"} {
		if !strings.Contains(buf.String(), s) {
			t.Errorf("text lacks %q:\n%s", s, buf.String())
		}
	}
}
