package main

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/stats"
	"github.com/samgozman/refetch/store"
)

// Tool names the hooks react to.
const (
	toolWebFetch  = "WebFetch"
	toolCtxFetch  = "mcp__plugin_context-mode_context-mode__ctx_fetch_and_index"
	toolFetchPage = "mcp__plugin_refetch_refetch__fetch_page"
	// toolProjectFetchPage is the name when refetch is a project's MCP server
	// called "refetch" instead of a plugin, as in cloud sessions.
	toolProjectFetchPage = "mcp__refetch__fetch_page"
	inlineChars          = 8000
	maxInline            = 3                // pages fetched inline per hook call
	inlineBudget         = 75 * time.Second // all inline fetches, under the 90 s hook timeout
	reasonChars          = 160              // a failure reason quoted to Claude
)

const standingGuidance = "If a web page cannot be read (WebFetch or ctx_fetch_and_index fails, is blocked, " +
	"or returns a challenge or empty page), call refetch's fetch_page tool with that URL before answering or giving up."

type hookInput struct {
	SessionID string `json:"session_id"`
	ToolUseID string `json:"tool_use_id"`
	Event     string `json:"hook_event_name"`
	Tool      string `json:"tool_name"`
	Input     struct {
		URL      string `json:"url"`
		Requests []struct {
			URL string `json:"url"`
		} `json:"requests"`
	} `json:"tool_input"`
	Response jsontext.Value `json:"tool_response"`
	Error    string         `json:"error"`
}

// failure is one URL a web read could not get, and why.
type failure struct{ url, reason string }

// runHook handles one hook event. It never fails the session: every error
// ends in exit 0 with no output, reported on stderr only with REFETCH_DEBUG.
func runHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) {
	debug := func(err error) {
		if err != nil && os.Getenv("REFETCH_DEBUG") != "" {
			_, _ = fmt.Fprintln(stderr, "refetch hook:", err)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			debug(fmt.Errorf("panic: %v", r))
		}
	}()
	cfg, err := config.Load()
	if err != nil {
		debug(err)
		return
	}
	useProxy(cfg)
	var in hookInput
	if err := json.UnmarshalRead(stdin, &in); err != nil {
		debug(err)
		return
	}
	if in.Event == "" && len(args) > 0 {
		in.Event = args[0]
	}
	var st *store.Store
	if cfg.Stats {
		if st, err = store.Open(ctx, store.Path(config.DataDir())); err != nil {
			debug(err)
		} else {
			defer func() { _ = st.Close() }()
		}
	}
	h := hooker{cfg: cfg, st: st, now: time.Now(), debug: debug}
	if text := h.handle(ctx, in); text != "" {
		out := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": in.Event, "additionalContext": text}}
		debug(json.MarshalWrite(stdout, out))
	}
}

type hooker struct {
	cfg   config.Config
	st    *store.Store // nil when stats are off
	now   time.Time
	debug func(error)
	fetch func(ctx context.Context, url string) (string, error) // inline mode; tests replace it
}

// handle records the event and returns the additionalContext to print, if
// any.
func (h *hooker) handle(ctx context.Context, in hookInput) string {
	switch in.Event {
	case "SessionStart":
		if h.st != nil {
			h.debug(h.st.Rollup(ctx, h.now.Add(-store.Retention)))
		}
		prunePages(config.DataDir(), savedPagesAge)
		return standingGuidance
	case "PreToolUse":
		if isRead(in.Tool) {
			for id, u := range readURLs(in) {
				h.record(ctx, in, id, u, store.CallStarted, "")
			}
		}
		return ""
	case "PostToolUse", "PostToolUseFailure":
	default:
		return ""
	}
	if stats.IsFetchPage(in.Tool) {
		outcome, why := store.CallOK, ""
		if in.Event == "PostToolUseFailure" {
			outcome, why = store.CallFailed, in.Error
		}
		h.record(ctx, in, in.ToolUseID, in.Input.URL, outcome, why)
		return ""
	}
	if !isRead(in.Tool) {
		return ""
	}
	failed := detect(in)
	for id, u := range readURLs(in) {
		outcome, why := store.CallOK, ""
		for _, f := range failed {
			if f.url == u {
				outcome, why = store.CallFailed, f.reason
			}
		}
		h.record(ctx, in, id, u, outcome, why)
	}
	if len(failed) == 0 {
		return ""
	}
	if h.cfg.Mode == "inline" {
		return h.inline(ctx, in, failed)
	}
	return nudge(in.Tool, failed)
}

func (h *hooker) record(ctx context.Context, in hookInput, id, url, outcome, why string) {
	h.recordAs(ctx, in.Tool, in, id, url, outcome, why)
}

func (h *hooker) recordAs(ctx context.Context, tool string, in hookInput, id, url, outcome, why string) {
	if h.st == nil {
		return
	}
	h.debug(h.st.RecordCall(ctx, store.ToolCall{
		Time: h.now, SessionID: in.SessionID, ToolUseID: id, Tool: tool, URL: url, Outcome: outcome, Error: why,
	}))
}

// fetchPageTool is fetch_page's full name: the plugin's, unless this hook
// runs outside a plugin in a cloud session.
func fetchPageTool() string {
	if os.Getenv("CLAUDE_PLUGIN_ROOT") == "" && config.Cloud() {
		return toolProjectFetchPage
	}
	return toolFetchPage
}

func isRead(tool string) bool { return tool == toolWebFetch || tool == toolCtxFetch }

// readURLs maps a row id to each URL a read asked for: the tool_use_id for
// one URL, tool_use_id#i for each request of a ctx_fetch_and_index batch.
func readURLs(in hookInput) map[string]string {
	if len(in.Input.Requests) == 0 {
		return map[string]string{in.ToolUseID: in.Input.URL}
	}
	out := map[string]string{}
	for i, r := range in.Input.Requests {
		out[in.ToolUseID+"#"+strconv.Itoa(i)] = r.URL
	}
	return out
}

// summaryBlock matches the wording of a challenge or refusal page in the
// text a read returned (WebFetch's summary or context-mode's preview).
var summaryBlock = regexp.MustCompile(`(?i)just a moment|checking your browser|verify(ing)? (that )?you are (a )?human|` +
	`attention required|access denied|enable javascript and cookies|captcha|are you a robot|unusual traffic`)

// summaryNeedsJS matches a summary saying the content needs JavaScript.
var summaryNeedsJS = regexp.MustCompile(`(?i)(enable|requires?|needs?|without|buil[dt]\w*|generat\w*|populat\w*|load\w*|render\w*|run\w*)[^.]{0,40}javascript`)

// summaryEmpty matches a summary saying the page had no content.
var summaryEmpty = regexp.MustCompile(`(?i)(content|page|response)[^.]{0,40}\b(is|was|came back|appears( to be)?) (empty|blank)|` +
	`\b(empty|blank) (page|content|response)|no (readable |visible |actual )?(text |page )?content`)

// ctxErrLine is one failed URL in a ctx_fetch_and_index batch result.
var ctxErrLine = regexp.MustCompile(`(?m)^- \[err\]\s+(\S+?):\s+(.+)$`)

// httpStatus is a status code in a batch failure reason.
var httpStatus = regexp.MustCompile(`\bHTTP (\d{3})\b`)

// batchReason reduces a batch failure reason to a fixed category: the line
// sits in text that also carries page content, so none of its words are
// quoted to Claude.
func batchReason(s string) string {
	if m := httpStatus.FindStringSubmatch(s); m != nil {
		return "HTTP " + m[1]
	}
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "timeout") || strings.Contains(low, "timed out"):
		return "timed out"
	case strings.Contains(low, "enotfound") || strings.Contains(low, "getaddrinfo") || strings.Contains(low, "dns"):
		return "DNS lookup failed"
	}
	return "fetch error"
}

// ctxSize is the indexed size context-mode reports for one page.
var ctxSize = regexp.MustCompile(`\((\d+(?:\.\d+)?)KB\)`)

// detect returns the URLs a web read failed to get.
func detect(in hookInput) []failure {
	urls := readURLs(in)
	if in.Event == "PostToolUseFailure" {
		reason := in.Error
		var out []failure
		for _, u := range urls {
			reason = strings.TrimPrefix(reason, "Failed to fetch "+u+": ")
			out = append(out, failure{u, cmp.Or(oneLine(reason, reasonChars), "tool error")})
		}
		return out
	}
	if in.Tool == toolWebFetch {
		var r struct {
			Code   int    `json:"code"`
			Bytes  int    `json:"bytes"`
			Result string `json:"result"`
		}
		if json.Unmarshal(in.Response, &r) != nil {
			return nil
		}
		switch {
		case r.Code >= 400:
			return []failure{{in.Input.URL, "HTTP " + strconv.Itoa(r.Code)}}
		case r.Code == 202:
			return []failure{{in.Input.URL, "HTTP 202, likely a bot challenge"}}
		case r.Bytes < 128<<10 && summaryBlock.MatchString(r.Result):
			return []failure{{in.Input.URL, "challenge or block page"}}
		case r.Bytes > 0 && (r.Bytes < 256 || (r.Bytes < 16<<10 && summaryNeedsJS.MatchString(r.Result))):
			return []failure{{in.Input.URL, "page needs JavaScript"}}
		case r.Bytes < 64<<10 && summaryEmpty.MatchString(r.Result):
			return []failure{{in.Input.URL, "page came back empty"}}
		}
		return nil
	}
	text := responseText(in.Response)
	if len(in.Input.Requests) > 0 {
		// Only URLs the batch asked for: the result text also carries page
		// content, which could forge an "[err]" line for any URL.
		requested := map[string]bool{}
		for _, u := range urls {
			requested[u] = true
		}
		var out []failure
		for _, m := range ctxErrLine.FindAllStringSubmatch(text, -1) {
			if requested[m[1]] {
				out = append(out, failure{m[1], batchReason(m[2])})
			}
		}
		return out
	}
	switch {
	case summaryBlock.MatchString(text):
		return []failure{{in.Input.URL, "challenge or block page"}}
	case strings.Contains(text, "Fetched and indexed") && tinyIndex(text):
		return []failure{{in.Input.URL, "page came back nearly empty (needs JavaScript?)"}}
	}
	return nil
}

func tinyIndex(text string) bool {
	m := ctxSize.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	kb, err := strconv.ParseFloat(m[1], 64)
	return err == nil && kb < 0.2
}

// responseText joins the text blocks of an MCP tool response.
func responseText(v jsontext.Value) string {
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(v, &blocks) != nil {
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
		return ""
	}
	var b strings.Builder
	for _, bl := range blocks {
		b.WriteString(bl.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func toolLabel(tool string) string {
	if tool == toolCtxFetch {
		return "ctx_fetch_and_index"
	}
	return tool
}

// nudge tells Claude, next to the failed result, to call fetch_page.
func nudge(tool string, failed []failure) string {
	var b strings.Builder
	for _, f := range failed {
		fmt.Fprintf(&b, "%s could not read %s (%s). Call fetch_page (%s) with this URL before answering or giving up. "+
			"Pass render: false if other sources will do.", toolLabel(tool), f.url, f.reason, fetchPageTool())
		if tool == toolCtxFetch {
			b.WriteString(" context-mode is active: pass save: true to keep the page out of context, then index the saved file with ctx_index.")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// inline fetches the failed pages in the hook and returns their text.
func (h *hooker) inline(ctx context.Context, in hookInput, failed []failure) string {
	ctx, cancel := context.WithTimeout(ctx, inlineBudget)
	defer cancel()
	get := h.fetch
	if get == nil {
		fr := newFetcherWith(h.cfg, h.st, "hook", nil)
		get = func(ctx context.Context, url string) (string, error) {
			return fetchPage(ctx, fr, config.DataDir(), fetchInput{URL: url, MaxChars: inlineChars})
		}
	}
	var b strings.Builder
	for i, f := range failed {
		if i == maxInline || ctx.Err() != nil {
			b.WriteString(nudge(in.Tool, failed[i:]))
			break
		}
		text, err := get(ctx, f.url)
		outcome := store.CallOK
		if err != nil {
			outcome = store.CallFailed
			fmt.Fprintf(&b, "%s could not read %s (%s), and refetch could not either: %v\n\n", toolLabel(in.Tool), f.url, f.reason, err)
		} else {
			fmt.Fprintf(&b, "%s could not read %s (%s). refetch fetched it instead:\n\n%s\n\n", toolLabel(in.Tool), f.url, f.reason, text)
		}
		h.recordAs(ctx, stats.InlineTool, in, in.ToolUseID+"#inline"+strconv.Itoa(i), f.url, outcome, errString(err))
	}
	return strings.TrimSpace(b.String())
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
