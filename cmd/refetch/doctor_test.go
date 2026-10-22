package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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
	fakeLatestRelease(t, http.StatusOK, `{"tag_name": "v1.4.0"}`)
	var out bytes.Buffer
	if err := runDoctor(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"! version      binary", "plugin 1.2.3",
		"! updates      refetch 1.2.3 is out of date; the latest release is 1.4.0",
		"✓ permission   fetch_page is allowed",
		"! chrome       no Chrome or Chromium found",
		"plugin context-mode@context-mode: PreToolUse on WebFetch (denies WebFetch",
		"~/.claude/settings.json: PostToolUse on WebFetch (RTK)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Bash") || strings.Contains(got, "plugin refetch@refetch") {
		t.Errorf("doctor lists unrelated hooks:\n%s", got)
	}
}

// fakeLatestRelease points doctor's release check at a loopback server.
func fakeLatestRelease(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	oldURL, oldClient := latestReleaseURL, releaseClient
	latestReleaseURL, releaseClient = srv.URL, srv.Client()
	t.Cleanup(func() { latestReleaseURL, releaseClient = oldURL, oldClient })
}

func TestUpdateCheck(t *testing.T) {
	for _, tc := range []struct {
		current, status, body string
		ok                    bool
		want                  string
	}{
		{"0.3.1", "200", `{"tag_name": "v0.3.1"}`, true, "0.3.1 is the latest release"},
		{"0.3.1", "200", `{"tag_name": "v0.4.2"}`, true, "0.3.1; the latest release is 0.4.2"},
		{"0.3.1", "200", `{"tag_name": "v0.5.0"}`, false, "refetch 0.3.1 is out of date; the latest release is 0.5.0"},
		{"0.9.0", "200", `{"tag_name": "v1.0.0"}`, false, "out of date"},
		{"1.0.0", "404", `{"message": "Not Found"}`, true, "could not check for a newer release: GitHub answered 404"},
		{"1.0.0", "200", `{"tag_name": "nightly"}`, true, `could not read the latest release "nightly"`},
		{"0.0.0-SNAPSHOT-554a66a", "200", `{"tag_name": "v1.0.0"}`, true, "not checked for a development build"},
		{"0.0.0", "200", `{"tag_name": "v1.0.0"}`, true, "not checked for a development build"},
	} {
		code := map[string]int{"200": http.StatusOK, "404": http.StatusNotFound}[tc.status]
		fakeLatestRelease(t, code, tc.body)
		t.Setenv("REFETCH_PLUGIN_VERSION", tc.current)
		c := updateCheck(t.Context())
		if c.ok != tc.ok || !strings.Contains(c.detail, tc.want) {
			t.Errorf("%s against %s: %+v, want ok=%v and %q", tc.current, tc.body, c, tc.ok, tc.want)
		}
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

// TestDoctorConfigDir: with CLAUDE_CONFIG_DIR (a second Claude Code profile)
// doctor reads that profile's settings and plugins, not ~/.claude.
func TestDoctorConfigDir(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, ".claude-work")
	for path, body := range map[string]string{
		filepath.Join(home, ".claude", "settings.json"): `{"permissions": {"allow": ["mcp__plugin_refetch_refetch__fetch_page"]}}`,
		filepath.Join(work, "settings.json"):            `{"hooks": {"PostToolUse": [{"matcher": "WebFetch", "hooks": [{"type": "command", "command": "rtk hook posthook"}]}]}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", work)
	t.Setenv("REFETCH_DATA", filepath.Join(home, "data"))
	t.Setenv("REFETCH_CONFIG", filepath.Join(home, "none.toml"))
	t.Setenv("REFETCH_CHROME", filepath.Join(home, "no-chrome"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	fakeLatestRelease(t, http.StatusOK, `{"tag_name": "v1.0.0"}`)
	var out bytes.Buffer
	if err := runDoctor(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"✓ claude       ~/.claude-work (from CLAUDE_CONFIG_DIR)",
		"! permission   fetch_page is not allowed",
		"Add to ~/.claude-work/settings.json",
		"~/.claude-work/settings.json: PostToolUse on WebFetch (RTK)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, got)
		}
	}
}
