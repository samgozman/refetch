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
