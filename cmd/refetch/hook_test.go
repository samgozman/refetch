package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/stats"
	"github.com/samgozman/refetch/store"
)

// Payloads in the shapes Claude Code 2.1.307 sends (recorded in the M0 spike).
const (
	webFetch403 = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"WebFetch","tool_use_id":"t1",
		"tool_input":{"url":"https://news.example.com/a?ref=x","prompt":"secret word?"},
		"tool_response":{"bytes":0,"code":403,"codeText":"Forbidden","result":"The server returned HTTP 403 Forbidden.","durationMs":186,"url":"https://news.example.com/a"},"duration_ms":186}`
	webFetchChallenge = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"WebFetch","tool_use_id":"t2",
		"tool_input":{"url":"https://news.example.com/b"},
		"tool_response":{"bytes":181,"code":200,"codeText":"OK","result":"It's a browser verification interstitial that says \"Just a moment...\"","durationMs":1810,"url":"https://news.example.com/b"}}`
	webFetchJS = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"WebFetch","tool_use_id":"t3",
		"tool_input":{"url":"https://app.example.com/"},
		"tool_response":{"bytes":2400,"code":200,"codeText":"OK","result":"The page has no content; it may build its content with JavaScript after loading.","durationMs":1697,"url":"https://app.example.com/"}}`
	webFetchOK = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"WebFetch","tool_use_id":"t4",
		"tool_input":{"url":"https://docs.example.com/"},
		"tool_response":{"bytes":48210,"code":200,"codeText":"OK","result":"The guide explains how to install the tool.","durationMs":900,"url":"https://docs.example.com/"}}`
	webFetchTimeout = `{"session_id":"s1","hook_event_name":"PostToolUseFailure","tool_name":"WebFetch","tool_use_id":"t5",
		"tool_input":{"url":"https://slow.example.com/"},"error":"timeout of 60000ms exceeded","is_interrupt":false,"duration_ms":60186}`
	webFetchPre = `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"WebFetch","tool_use_id":"t6","tool_input":{"url":"https://c.example.com/"}}`
	ctx403      = `{"session_id":"s1","hook_event_name":"PostToolUseFailure","tool_name":"mcp__plugin_context-mode_context-mode__ctx_fetch_and_index","tool_use_id":"t7",
		"tool_input":{"url":"https://c.example.com/"},"error":"Failed to fetch https://c.example.com/: HTTP 403","is_interrupt":false}`
	ctxChallenge = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"mcp__plugin_context-mode_context-mode__ctx_fetch_and_index","tool_use_id":"t8",
		"tool_input":{"url":"https://d.example.com/"},
		"tool_response":[{"type":"text","text":"Fetched and indexed **1 sections** (0.1KB) from: https://d.example.com/\n---\n\nJust a moment...\n\nChecking your browser before accessing."}]}`
	ctxJS = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"mcp__plugin_context-mode_context-mode__ctx_fetch_and_index","tool_use_id":"t9",
		"tool_input":{"url":"https://e.example.com/"},
		"tool_response":[{"type":"text","text":"Fetched and indexed **1 sections** (0.0KB) from: https://e.example.com/\n---\n\nApp"}]}`
	ctxBatch = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"mcp__plugin_context-mode_context-mode__ctx_fetch_and_index","tool_use_id":"t10",
		"tool_input":{"requests":[{"url":"https://f.example.com/ok","source":"ok"},{"url":"https://f.example.com/403","source":"403"}]},
		"tool_response":[{"type":"text","text":"fetched 2 c=1. ok=1 cache=0 err=1. 2 sections 0.1KB.\n\n- [new]   ok::https://f.example.com/ok — 2 sections (0.1KB)\n- [err]   https://f.example.com/403: HTTP 403\n"}]}`
	fetchPageOK = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"mcp__plugin_refetch_refetch__fetch_page","tool_use_id":"t11",
		"tool_input":{"url":"https://news.example.com/a"},"tool_response":[{"type":"text","text":"Source: ..."}]}`
	fetchPageFail = `{"session_id":"s1","hook_event_name":"PostToolUseFailure","tool_name":"mcp__plugin_refetch_refetch__fetch_page","tool_use_id":"t12",
		"tool_input":{"url":"https://c.example.com/"},"error":"could not read https://c.example.com/: refused (HTTP 403, chrome_like client)"}`
	sessionStart = `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`
	bashPost     = `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"t13","tool_input":{"command":"ls"},"tool_response":{"stdout":"x"}}`
)

func parse(t *testing.T, s string) hookInput {
	t.Helper()
	var in hookInput
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestHookNudges(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h := hooker{cfg: config.Default(), st: st, now: time.Now(), debug: func(err error) {
		if err != nil {
			t.Error(err)
		}
	}}
	cases := []struct {
		name, payload string
		want          []string // substrings of the nudge; nil = no output
	}{
		{"WebFetch 403", webFetch403, []string{"WebFetch could not read https://news.example.com/a?ref=x (HTTP 403)", toolFetchPage, "render: false"}},
		{"WebFetch challenge summary", webFetchChallenge, []string{"challenge or block page"}},
		{"WebFetch JavaScript shell", webFetchJS, []string{"page needs JavaScript"}},
		{"WebFetch ok", webFetchOK, nil},
		{"WebFetch timeout", webFetchTimeout, []string{"(timeout of 60000ms exceeded)"}},
		{"WebFetch pre", webFetchPre, nil},
		{"ctx 403", ctx403, []string{"ctx_fetch_and_index could not read https://c.example.com/ (HTTP 403)", "save: true"}},
		{"ctx challenge preview", ctxChallenge, []string{"https://d.example.com/ (challenge or block page)"}},
		{"ctx empty page", ctxJS, []string{"nearly empty"}},
		{"ctx batch", ctxBatch, []string{"https://f.example.com/403 (HTTP 403)"}},
		{"fetch_page ok", fetchPageOK, nil},
		{"fetch_page failed", fetchPageFail, nil},
		{"SessionStart", sessionStart, []string{"fetch_page"}},
		{"Bash", bashPost, nil},
	}
	for _, tc := range cases {
		got := h.handle(t.Context(), parse(t, tc.payload))
		if tc.want == nil && got != "" {
			t.Errorf("%s: unexpected output %q", tc.name, got)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: output %q lacks %q", tc.name, got, w)
			}
		}
		if tc.name == "ctx batch" && strings.Contains(got, "/ok") {
			t.Errorf("batch nudged for the URL that worked: %q", got)
		}
	}

	calls, err := st.Calls(t.Context(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]store.ToolCall{}
	for _, c := range calls {
		byID[c.ToolUseID] = c
	}
	checks := map[string]string{
		"t1": store.CallFailed, "t2": store.CallFailed, "t3": store.CallFailed, "t4": store.CallOK, "t5": store.CallFailed,
		"t6": store.CallStarted, "t7": store.CallFailed, "t10#0": store.CallOK, "t10#1": store.CallFailed,
		"t11": store.CallOK, "t12": store.CallFailed,
	}
	for id, want := range checks {
		if byID[id].Outcome != want {
			t.Errorf("row %s = %+v, want outcome %s", id, byID[id], want)
		}
	}
	if byID["t1"].URL != "https://news.example.com/a" || byID["t7"].Error != "HTTP 403" {
		t.Errorf("rows: t1 %+v, t7 %+v", byID["t1"], byID["t7"])
	}
	if _, ok := byID["t13"]; ok {
		t.Error("Bash must not be recorded")
	}
	r := stats.Build(calls, nil, stats.Options{}, time.Now())
	if r.Failed != 8 || r.PickedUp != 2 || r.Resolved != 1 || r.StillFailed != 1 {
		t.Errorf("funnel from hook rows: %+v", r)
	}
}

func TestHookInline(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "inline"
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h := hooker{cfg: cfg, st: st, now: time.Now(), debug: func(error) {}, fetch: func(_ context.Context, url string) (string, error) {
		if strings.Contains(url, "f.example.com/403") {
			return "", errors.New("refused (HTTP 403, chrome_like client)")
		}
		return "Source: " + url + "\n\nthe real page", nil
	}}
	got := h.handle(t.Context(), parse(t, webFetch403))
	if !strings.Contains(got, "refetch fetched it instead") || !strings.Contains(got, "the real page") {
		t.Errorf("inline output %q", got)
	}
	got = h.handle(t.Context(), parse(t, ctxBatch))
	if !strings.Contains(got, "refetch could not either") {
		t.Errorf("inline failure output %q", got)
	}
	calls, _ := st.Calls(t.Context(), time.Time{})
	r := stats.Build(calls, nil, stats.Options{}, time.Now())
	if r.PickedUp != 2 || r.Resolved != 1 || r.StillFailed != 1 {
		t.Errorf("inline rescues in the funnel: %+v", r)
	}
}

func TestRunHookNeverFails(t *testing.T) {
	t.Setenv("REFETCH_DATA", t.TempDir())
	t.Setenv("REFETCH_CONFIG", "/nonexistent/config.toml")
	for _, in := range []string{"", "not json", `{"hook_event_name":"PostToolUse","tool_name":"WebFetch","tool_response":"weird"}`, webFetch403} {
		var out, errOut bytes.Buffer
		runHook(t.Context(), nil, strings.NewReader(in), &out, &errOut)
		if in == webFetch403 {
			var v struct {
				HookSpecificOutput struct {
					HookEventName     string `json:"hookEventName"`
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.HookSpecificOutput.HookEventName != "PostToolUse" || v.HookSpecificOutput.AdditionalContext == "" {
				t.Errorf("hook output %q: %v", out.String(), err)
			}
		} else if out.Len() != 0 {
			t.Errorf("input %q: output %q", in, out.String())
		}
	}
	t.Setenv("REFETCH_MODE", "loud") // invalid config: still exit quietly
	var out bytes.Buffer
	runHook(t.Context(), nil, strings.NewReader(webFetch403), &out, os.Stderr)
	if out.Len() != 0 {
		t.Errorf("invalid config produced %q", out.String())
	}
}
