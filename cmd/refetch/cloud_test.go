package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
)

// cloudProxy fakes a cloud session's proxy: it answers every CONNECT with
// status and closes the tunnel.
func cloudProxy(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return "http://" + srv.Listener.Addr().String()
}

func cloudSession(t *testing.T, proxy string) {
	t.Helper()
	t.Cleanup(func() { fetch.UseProxy() }) // after the variables below are restored
	t.Setenv("CLAUDE_CODE_REMOTE", "true")
	for _, k := range []string{"https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("HTTPS_PROXY", proxy)
	old := probeHost
	probeHost = "refetch-test.invalid:443"
	t.Cleanup(func() { probeHost = old })
}

func TestNetworkCheck(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		t.Setenv("CLAUDE_CODE_REMOTE", "")
		t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
		cfg := config.Default()
		if c := networkCheck(t.Context(), cfg, useProxy(cfg)); !c.ok || c.detail != "direct connections" {
			t.Errorf("a local proxy is ignored without use_proxy: %+v", c)
		}
	})
	t.Run("cloud, all sites", func(t *testing.T) {
		cloudSession(t, cloudProxy(t, http.StatusOK))
		cfg := config.Default()
		proxy := useProxy(cfg)
		if c := networkCheck(t.Context(), cfg, proxy); !c.ok || !strings.Contains(c.detail, "sites beyond the default list are reachable") {
			t.Errorf("open network: %+v", c)
		}
	})
	t.Run("cloud, listed domains only", func(t *testing.T) {
		cloudSession(t, cloudProxy(t, http.StatusForbidden))
		cfg := config.Default()
		c := networkCheck(t.Context(), cfg, useProxy(cfg))
		if c.ok || !strings.Contains(c.detail, "network access level allows only listed domains") {
			t.Errorf("allowlisted network: %+v", c)
		}
	})
	t.Run("cloud, no proxy", func(t *testing.T) {
		cloudSession(t, "")
		cfg := config.Default()
		if c := networkCheck(t.Context(), cfg, useProxy(cfg)); c.ok {
			t.Errorf("a cloud session without a proxy: %+v", c)
		}
	})
	t.Run("use_proxy", func(t *testing.T) {
		t.Cleanup(func() { fetch.UseProxy() })
		t.Setenv("CLAUDE_CODE_REMOTE", "")
		t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
		cfg := config.Default()
		cfg.UseProxy = true
		if c := networkCheck(t.Context(), cfg, useProxy(cfg)); !c.ok || c.detail != "through the proxy at http://127.0.0.1:3128 (use_proxy)" {
			t.Errorf("use_proxy: %+v", c)
		}
	})
}

func TestFetchPageToolName(t *testing.T) {
	for _, tc := range []struct{ remote, pluginRoot, want string }{
		{"", "/plugins/refetch", toolFetchPage},
		{"", "", toolFetchPage},
		{"true", "/plugins/refetch", toolFetchPage},
		{"true", "", toolProjectFetchPage},
	} {
		t.Setenv("CLAUDE_CODE_REMOTE", tc.remote)
		t.Setenv("CLAUDE_PLUGIN_ROOT", tc.pluginRoot)
		if got := fetchPageTool(); got != tc.want {
			t.Errorf("remote %q, plugin root %q: %s", tc.remote, tc.pluginRoot, got)
		}
	}
}

func TestOwnHooksAreNotOtherHooks(t *testing.T) {
	own := hookGroup{Matcher: "WebFetch|mcp__refetch__.*"}
	own.Hooks = append(own.Hooks, struct {
		Command string `json:"command"`
	}{"refetch hook PostToolUse"})
	if line := own.line(".claude/settings.json", "PostToolUse"); line != "" {
		t.Errorf("refetch's own project hook listed: %q", line)
	}
}

func TestPermissionAdviceInCloud(t *testing.T) {
	t.Setenv("CLAUDE_CODE_REMOTE", "true")
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	e := claudeEnv{home: t.TempDir(), cwd: t.TempDir(), config: t.TempDir()}
	c := e.permissionCheck()
	if c.ok || !strings.Contains(c.detail, `Add to the repository's .claude/settings.json: "permissions": {"allow": ["mcp__refetch__fetch_page"]}`) {
		t.Errorf("cloud permission advice: %+v", c)
	}
}

func TestOwnHooksCheck(t *testing.T) {
	e := claudeEnv{home: t.TempDir(), cwd: t.TempDir(), config: t.TempDir()}
	if c := e.ownHooksCheck(); c.ok || !strings.Contains(c.detail, "no refetch hook for SessionStart, PreToolUse, PostToolUse, PostToolUseFailure") {
		t.Errorf("no hooks: %+v", c)
	}
	settings := `{"hooks": {
		"SessionStart": [{"hooks": [{"type": "command", "command": "/opt/refetch/bin/refetch hook SessionStart"}]}],
		"PreToolUse": [{"matcher": "WebFetch", "hooks": [{"type": "command", "command": "/opt/refetch/bin/refetch hook PreToolUse"}]}],
		"PostToolUse": [{"matcher": "WebFetch", "hooks": [{"type": "command", "command": "/opt/refetch/bin/refetch hook PostToolUse"}]}]
	}}`
	if err := os.MkdirAll(filepath.Join(e.cwd, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.cwd, ".claude", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := e.ownHooksCheck(); c.ok || !strings.Contains(c.detail, "no refetch hook for PostToolUseFailure in") {
		t.Errorf("one hook missing: %+v", c)
	}
	settings = strings.Replace(settings, "}]\n\t}}", `}],
		"PostToolUseFailure": [{"matcher": "WebFetch", "hooks": [{"type": "command", "command": "/opt/refetch/bin/refetch hook PostToolUseFailure"}]}]
	}}`, 1)
	if err := os.WriteFile(filepath.Join(e.cwd, ".claude", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := e.ownHooksCheck(); !c.ok || !strings.HasPrefix(c.detail, "refetch's hooks are in ") {
		t.Errorf("all hooks: %+v", c)
	}
}

func TestVersionOfADevelopmentCheckout(t *testing.T) {
	t.Setenv("REFETCH_PLUGIN_VERSION", "0.0.0")
	if c := versionCheck(); !c.ok {
		t.Errorf("REFETCH_BIN with a development checkout: %+v", c)
	}
}
