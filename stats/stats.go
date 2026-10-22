// Package stats turns events.db rows into the refetch stats report: how many
// web reads failed, how many refetch picked up and how many it resolved, and
// which step won.
package stats

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samgozman/refetch/store"
)

// PickupWindow is how long after a failed read a fetch_page call for the
// same URL still counts as picking it up.
const PickupWindow = 10 * time.Minute

// Report is the efficiency report. Counts are of web reads unless named
// otherwise.
type Report struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Sessions int       `json:"sessions"`

	Reads       int            `json:"reads"`
	OKFirst     int            `json:"ok_first_time"`
	Failed      int            `json:"failed"`
	PickedUp    int            `json:"picked_up"`
	Resolved    int            `json:"resolved"`
	ResolvedBy  map[string]int `json:"resolved_by,omitempty"` // winning client
	StillFailed int            `json:"still_failed"`
	FailedWhy   map[string]int `json:"still_failed_by,omitempty"` // last attempt's outcome
	NotPickedUp int            `json:"not_picked_up"`
	Direct      int            `json:"direct_fetch_page"`
	DirectOK    int            `json:"direct_fetch_page_ok"`
	TopHosts    []HostCount    `json:"top_hosts,omitempty"` // hosts of picked-up reads

	HeadlessWins map[string]int `json:"headless_wins_by_trigger,omitempty"` // resolved by headless Chrome, by what sent the page there

	Fetches  Fetches   `json:"fetches"`         // every fetch_page and refetch get, from the attempts table
	Headless Headless  `json:"headless"`        // the headless Chrome step over all fetches
	Hosts    []HostRow `json:"hosts,omitempty"` // filled for --by host
}

// Fetches summarizes the attempts table by fetch: what fetch_page (and
// refetch get) did, whether or not a hook saw the call.
type Fetches struct {
	Total    int            `json:"total"`
	OK       int            `json:"ok"`
	OKBy     map[string]int `json:"ok_by,omitempty"`
	FailedBy map[string]int `json:"failed_by,omitempty"`
	MedianMS int64          `json:"median_ms"`
}

// Headless counts the headless Chrome step: needed whenever the HTTP clients
// got a challenge or a JavaScript-only page, then either ran or was skipped.
type Headless struct {
	Needed        int   `json:"needed"`
	Ran           int   `json:"ran"`
	OK            int   `json:"ok"`
	TimedOut      int   `json:"timed_out"`
	Failed        int   `json:"failed"`
	SkippedByCall int   `json:"skipped_by_call"`
	SkippedOff    int   `json:"skipped_off"`
	NoChrome      int   `json:"no_chrome"`
	SkippedRoot   int   `json:"skipped_root"`
	MedianMS      int64 `json:"median_ms"` // of the runs
}

// HostCount is a host and how often it needed refetch.
type HostCount struct {
	Host string `json:"host"`
	N    int    `json:"n"`
}

// HostRow is one host's funnel.
type HostRow struct {
	Host     string `json:"host"`
	Reads    int    `json:"reads"`
	Failed   int    `json:"failed"`
	PickedUp int    `json:"picked_up"`
	Resolved int    `json:"resolved"`
	Fetches  int    `json:"fetches"`
}

// Options filter the report.
type Options struct {
	Since   time.Time
	Session string // only this Claude Code session
	ByHost  bool
}

// readTools are the tools whose calls are web reads.
var readTools = map[string]bool{
	"WebFetch": true,
	"mcp__plugin_context-mode_context-mode__ctx_fetch_and_index": true,
}

// InlineTool is the tool name recorded for a page the hook fetched itself
// (mode = "inline"); it counts as a fetch_page call.
const InlineTool = "refetch:inline"

// IsFetchPage reports whether a tool name is refetch's fetch_page, as a
// plugin (mcp__plugin_refetch_refetch__fetch_page) or added with claude mcp
// add (mcp__refetch__fetch_page), or an inline fetch by the hook.
func IsFetchPage(tool string) bool {
	return tool == InlineTool ||
		strings.HasPrefix(tool, "mcp__") && strings.Contains(tool, "refetch") && strings.HasSuffix(tool, "__fetch_page")
}

type fetch struct {
	url, host  string
	end        time.Time
	ok         bool
	winner     string
	why        string
	durationMS int64
	headless   string // the headless step's outcome; "" when it was not needed
	trigger    string // the outcome that sent the page to the headless step
	headlessMS int64
	last       string // the last HTTP client outcome
}

// stepTwoWhy names a failed headless step for the still-failed breakdown.
var stepTwoWhy = map[string]string{
	"timeout": "step 2 timed out", "skipped_off": "step 2 skipped", "skipped_by_call": "step 2 skipped",
	"no_chrome": "step 2 skipped", "skipped_root": "step 2 skipped",
}

// Build computes the report from rows already filtered by time.
func Build(calls []store.ToolCall, attempts []store.Attempt, opt Options, now time.Time) Report {
	if opt.Session != "" {
		calls = slices.DeleteFunc(slices.Clone(calls), func(c store.ToolCall) bool { return c.SessionID != opt.Session })
	}
	b := builder{
		r:       Report{From: opt.Since, To: now, ResolvedBy: map[string]int{}, FailedWhy: map[string]int{}, HeadlessWins: map[string]int{}},
		hosts:   map[string]*HostRow{},
		needed:  map[string]int{},
		fetches: groupFetches(attempts),
	}
	sessions := map[string]bool{}
	var reads, started []store.ToolCall
	for _, c := range calls {
		if c.SessionID != "" {
			sessions[c.SessionID] = true
		}
		switch {
		case readTools[c.Tool] && (c.Outcome == store.CallOK || c.Outcome == store.CallFailed):
			reads = append(reads, c)
		case c.Tool == "WebFetch" && c.Outcome == store.CallStarted:
			started = append(started, c)
		case IsFetchPage(c.Tool) && c.Outcome != store.CallStarted:
			b.pages = append(b.pages, c)
		}
	}
	reads = append(reads, b.redirected(started, reads)...)
	slices.SortStableFunc(reads, func(x, y store.ToolCall) int { return x.Time.Compare(y.Time) })
	b.r.Sessions = len(sessions)
	b.claimed = make([]bool, len(b.pages))
	for _, rd := range reads {
		b.read(rd)
	}
	for i, p := range b.pages {
		if !b.claimed[i] {
			b.r.Direct++
			if p.Outcome == store.CallOK {
				b.r.DirectOK++
			}
		}
	}
	b.finish(opt)
	return b.r
}

type builder struct {
	r       Report
	hosts   map[string]*HostRow
	needed  map[string]int // picked-up reads per host
	fetches []fetch
	pages   []store.ToolCall // fetch_page calls
	claimed []bool           // pages already matched to a failed read
}

func (b *builder) row(host string) *HostRow {
	if b.hosts[host] == nil {
		b.hosts[host] = &HostRow{Host: host}
	}
	return b.hosts[host]
}

// read counts one web read and, when it failed, the fetch_page call that
// picked it up.
func (b *builder) read(rd store.ToolCall) {
	b.r.Reads++
	b.row(rd.Host).Reads++
	if rd.Outcome == store.CallOK {
		b.r.OKFirst++
		return
	}
	b.r.Failed++
	b.row(rd.Host).Failed++
	i := b.pickup(rd)
	if i < 0 {
		b.r.NotPickedUp++
		return
	}
	b.claimed[i] = true
	b.r.PickedUp++
	b.row(rd.Host).PickedUp++
	b.needed[rd.Host]++
	f := matchFetch(b.fetches, b.pages[i])
	if b.pages[i].Outcome == store.CallOK {
		b.r.Resolved++
		b.row(rd.Host).Resolved++
		b.r.ResolvedBy[cmp.Or(f.winner, "unknown")]++
		if f.winner == "headless" {
			b.r.HeadlessWins[f.trigger]++
		}
	} else {
		b.r.StillFailed++
		b.r.FailedWhy[cmp.Or(f.why, "unknown")]++
	}
}

// redirected turns a WebFetch that another hook denied (a PreToolUse with no
// post event, as context-mode does) into a failed read when Claude went
// straight to fetch_page for the same URL instead of the tool it was sent to.
// A denied WebFetch followed by ctx_fetch_and_index is that read instead.
func (b *builder) redirected(started, reads []store.ToolCall) []store.ToolCall {
	near := func(c, d store.ToolCall) bool {
		return c.SessionID == d.SessionID && c.URL == d.URL && !d.Time.Before(c.Time) && d.Time.Sub(c.Time) <= PickupWindow
	}
	var out []store.ToolCall
	for _, c := range started {
		if slices.ContainsFunc(reads, func(r store.ToolCall) bool { return near(c, r) }) ||
			!slices.ContainsFunc(b.pages, func(p store.ToolCall) bool { return near(c, p) }) {
			continue
		}
		c.Outcome, c.Error = store.CallFailed, "redirected"
		out = append(out, c)
	}
	return out
}

// pickup is the index of the first unclaimed fetch_page call for the same
// URL in the same session within PickupWindow after rd, or -1.
func (b *builder) pickup(rd store.ToolCall) int {
	for j, p := range b.pages {
		if !b.claimed[j] && p.SessionID == rd.SessionID && p.URL == rd.URL && !p.Time.Before(rd.Time) && p.Time.Sub(rd.Time) <= PickupWindow {
			return j
		}
	}
	return -1
}

func (b *builder) finish(opt Options) {
	for h, n := range b.needed {
		b.r.TopHosts = append(b.r.TopHosts, HostCount{h, n})
	}
	slices.SortFunc(b.r.TopHosts, func(x, y HostCount) int { return cmp.Or(cmp.Compare(y.N, x.N), cmp.Compare(x.Host, y.Host)) })
	b.r.TopHosts = b.r.TopHosts[:min(len(b.r.TopHosts), 5)]
	if opt.Session == "" {
		b.r.Fetches = summarize(b.fetches)
		b.r.Headless = headless(b.fetches)
		for _, f := range b.fetches {
			b.row(f.host).Fetches++
		}
	}
	if opt.ByHost {
		for _, h := range slices.Sorted(maps.Keys(b.hosts)) {
			b.r.Hosts = append(b.r.Hosts, *b.hosts[h])
		}
		slices.SortStableFunc(b.r.Hosts, func(x, y HostRow) int { return cmp.Compare(y.Failed+y.Fetches, x.Failed+x.Fetches) })
	}
}

// groupFetches folds attempts into one record per fetch_id.
func groupFetches(attempts []store.Attempt) []fetch {
	idx := map[string]int{}
	var out []fetch
	for _, a := range attempts {
		i, ok := idx[a.FetchID]
		if !ok {
			i = len(out)
			idx[a.FetchID] = i
			out = append(out, fetch{url: a.URL, host: a.Host})
		}
		f := &out[i]
		f.end = a.Time
		f.durationMS += a.Duration.Milliseconds()
		if a.Client == "headless" {
			f.headless, f.trigger, f.headlessMS = a.Outcome, f.last, a.Duration.Milliseconds()
		} else {
			f.last = a.Outcome
		}
		switch {
		case a.Outcome == "ok":
			f.ok, f.winner = true, a.Client
		case f.ok:
		case a.Client == "headless":
			f.why = cmp.Or(stepTwoWhy[a.Outcome], "step 2 failed")
		default:
			f.why = a.Outcome
			if a.Outcome == "error" && a.Status != 0 {
				f.why = "HTTP " + strconv.Itoa(a.Status)
			}
		}
	}
	return out
}

// matchFetch finds the fetch behind a fetch_page call: same URL, ended
// within five minutes before the hook recorded the call.
func matchFetch(fetches []fetch, call store.ToolCall) fetch {
	var best fetch
	for _, f := range fetches {
		if f.url == call.URL && !f.end.After(call.Time.Add(5*time.Second)) && call.Time.Sub(f.end) <= 5*time.Minute {
			best = f
		}
	}
	return best
}

func summarize(fetches []fetch) Fetches {
	s := Fetches{OKBy: map[string]int{}, FailedBy: map[string]int{}}
	var ds []int64
	for _, f := range fetches {
		s.Total++
		ds = append(ds, f.durationMS)
		if f.ok {
			s.OK++
			s.OKBy[f.winner]++
		} else {
			s.FailedBy[f.why]++
		}
	}
	if len(ds) > 0 {
		slices.Sort(ds)
		s.MedianMS = ds[len(ds)/2]
	}
	return s
}

func headless(fetches []fetch) Headless {
	var h Headless
	var ds []int64
	for _, f := range fetches {
		switch f.headless {
		case "":
			continue
		case "skipped_by_call":
			h.SkippedByCall++
		case "skipped_off":
			h.SkippedOff++
		case "no_chrome":
			h.NoChrome++
		case "skipped_root":
			h.SkippedRoot++
		default:
			h.Ran++
			ds = append(ds, f.headlessMS)
			switch f.headless {
			case "ok":
				h.OK++
			case "timeout":
				h.TimedOut++
			default:
				h.Failed++
			}
		}
		h.Needed++
	}
	if len(ds) > 0 {
		slices.Sort(ds)
		h.MedianMS = ds[len(ds)/2]
	}
	return h
}
