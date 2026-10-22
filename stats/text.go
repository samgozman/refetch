package stats

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

var clientNames = map[string]string{
	"chrome_like":  "Chrome-like client",
	"plain":        "plain client",
	"plain_backup": "plain backup",
	"headless":     "headless Chrome",
}

// Header is context outside the report data.
type Header struct {
	Version       string
	DataPath      string
	Bytes         int64
	Chrome        string // like "Chrome 155"; "" when none was found
	RenderTimeout string // like "15s"
}

var triggerNames = map[string]string{"challenge": "challenge", "needs_js": "JS-only"}

// WriteText writes the report as the plain-text table refetch stats prints.
func (r Report) WriteText(w io.Writer, h Header) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	line := func(indent int, label string, n int, note string) {
		p("%-44s %5d   %s\n", strings.Repeat("  ", indent)+label, n, note)
	}
	sessions := ""
	if r.Sessions > 0 {
		sessions = fmt.Sprintf(" · %d sessions", r.Sessions)
	}
	p("refetch %s · %s to %s%s\n\n", h.Version, r.From.Format("2 Jan"), r.To.Format("2 Jan 2006"), sessions)

	if r.Reads == 0 && r.Direct == 0 {
		p("No web reads recorded yet: the refetch plugin's hooks record WebFetch and ctx_fetch_and_index calls.\n")
	} else {
		line(0, "Web reads (WebFetch + ctx_fetch_and_index)", r.Reads, "")
		line(1, "ok first time", r.OKFirst, pct(r.OKFirst, r.Reads, ""))
		line(1, "failed", r.Failed, pct(r.Failed, r.Reads, ""))
		line(2, "picked up by refetch", r.PickedUp, pct(r.PickedUp, r.Failed, " of failed"))
		line(3, "resolved", r.Resolved, pct(r.Resolved, r.PickedUp, " of picked up"))
		for _, c := range sortedKeys(r.ResolvedBy) {
			note := ""
			if c == "headless" {
				wins := map[string]int{}
				for t, n := range r.HeadlessWins {
					wins[cmp.Or(triggerNames[t], t)] = n
				}
				note = breakdown(wins)
			}
			line(4, cmp.Or(clientNames[c], c), r.ResolvedBy[c], note)
		}
		line(3, "still failed", r.StillFailed, breakdown(r.FailedWhy))
		line(2, "not picked up", r.NotPickedUp, "")
		line(0, "Direct fetch_page calls", r.Direct, fmt.Sprintf("%d ok", r.DirectOK))
	}

	if f := r.Fetches; f.Total > 0 {
		p("\n")
		okBy := map[string]int{}
		for c, n := range f.OKBy {
			okBy[cmp.Or(clientNames[c], c)] = n
		}
		line(0, "Fetches (fetch_page and refetch get)", f.Total, "median "+millis(f.MedianMS))
		line(1, "ok", f.OK, breakdown(okBy))
		line(1, "failed", f.Total-f.OK, breakdown(f.FailedBy))
	}

	if hd := r.Headless; hd.Needed > 0 {
		p("\nHeadless Chrome step (%s, timeout %s)\n", cmp.Or(h.Chrome, "no Chrome found"), cmp.Or(h.RenderTimeout, "15s"))
		line(1, "needed", hd.Needed, "")
		ran := fmt.Sprintf("ok %d · timed out %d · failed %d", hd.OK, hd.TimedOut, hd.Failed)
		if hd.Ran > 0 {
			ran += " · median " + millis(hd.MedianMS)
		}
		line(1, "ran", hd.Ran, ran)
		skipped := fmt.Sprintf("by call %d · off %d · no Chrome %d", hd.SkippedByCall, hd.SkippedOff, hd.NoChrome)
		if hd.SkippedRoot > 0 {
			skipped += fmt.Sprintf(" · root %d", hd.SkippedRoot)
		}
		line(1, "skipped", hd.SkippedByCall+hd.SkippedOff+hd.NoChrome+hd.SkippedRoot, skipped)
	}

	if len(r.Hosts) > 0 {
		p("\n%-32s %6s %7s %10s %9s %8s\n", "Host", "reads", "failed", "picked up", "resolved", "fetches")
		for _, hr := range r.Hosts {
			p("%-32s %6d %7d %10d %9d %8d\n", hr.Host, hr.Reads, hr.Failed, hr.PickedUp, hr.Resolved, hr.Fetches)
		}
	} else if len(r.TopHosts) > 0 {
		var parts []string
		for _, hc := range r.TopHosts {
			parts = append(parts, fmt.Sprintf("%s (%d)", hc.Host, hc.N))
		}
		p("\nTop hosts that needed refetch: %s\n", strings.Join(parts, ", "))
	}
	p("Data: %s (%s) · refetch stats --help\n", h.DataPath, size(h.Bytes))
}

func pct(n, of int, suffix string) string {
	if of == 0 {
		return ""
	}
	return fmt.Sprintf("%3d%%%s", n*100/of, suffix)
}

// breakdown renders "refused 5 · HTTP 404 1", largest first.
func breakdown(m map[string]int) string {
	keys := sortedKeys(m)
	slices.SortStableFunc(keys, func(a, b string) int { return cmp.Compare(m[b], m[a]) })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if m[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
		}
	}
	return strings.Join(parts, " · ")
}

func sortedKeys(m map[string]int) []string { return slices.Sorted(maps.Keys(m)) }

func millis(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

func size(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%d KB", b>>10)
	}
	return fmt.Sprintf("%d B", b)
}
