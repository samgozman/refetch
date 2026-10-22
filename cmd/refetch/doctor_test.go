package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctor(t *testing.T) {
	home := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cm := filepath.Join(home, "plugins", "context-mode")
	write(filepath.Join(home, ".claude", "settings.json"), `{
		"permissions": {"allow": ["mcp__plugin_refetch_refetch__fetch_page"]},
		"enabledPlugins": {"context-mode@context-mode": true, "refetch@refetch": true},
		"hooks": {"PostToolUse": [{"matcher": "WebFetch|WebSearch", "hooks": [{"type": "command", "command": "rtk hook posthook"}]}],
		          "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "rtk hook claude"}]}]}}`)
	write(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{"version": 2, "plugins": {
		"context-mode@context-mode": [{"scope": "user", "installPath": "`+cm+`"}],
		"refetch@refetch": [{"scope": "user", "installPath": "/nowhere"}]}}`)
	write(filepath.Join(cm, "hooks", "hooks.json"), `{"hooks": {"PreToolUse": [{"matcher": "WebFetch", "hooks": [{"type": "command", "command": "node x"}]}]}}`)

	t.Setenv("HOME", home)
	t.Setenv("REFETCH_DATA", filepath.Join(home, "data"))
	t.Setenv("REFETCH_CONFIG", filepath.Join(home, "none.toml"))
	t.Setenv("REFETCH_CHROME", filepath.Join(home, "no-chrome"))
	t.Setenv("REFETCH_PLUGIN_VERSION", "1.2.3")
	t.Chdir(home)
	var out bytes.Buffer
	if err := runDoctor(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"! version      binary", "plugin 1.2.3",
		"✓ permission   fetch_page is allowed",
		"! chrome       no Chrome or Chromium found",
		"plugin context-mode@context-mode: PreToolUse on WebFetch (denies WebFetch",
		"~/.claude/settings.json: PostToolUse on WebFetch (RTK)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Bash") || strings.Contains(got, "refetch@refetch") {
		t.Errorf("doctor lists unrelated hooks:\n%s", got)
	}
}

func TestMatches(t *testing.T) {
	for _, tc := range []struct {
		m, tool string
		want    bool
	}{
		{"", "WebFetch", true}, {"*", "WebFetch", true}, {"WebFetch", "WebFetch", true},
		{"WebFetch|WebSearch", "WebFetch", true}, {"Web", "WebFetch", false}, {"mcp__.*", toolFetchPage, true},
		{"Bash", "WebFetch", false}, {"(", "(", true},
	} {
		if got := matches(tc.m, tc.tool); got != tc.want {
			t.Errorf("matches(%q, %q) = %v", tc.m, tc.tool, got)
		}
	}
}
