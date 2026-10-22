package refetch_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPluginManifests checks that the hooks call the shim for the events in
// the plan and that the MCP server runs through the shim.
func TestPluginManifests(t *testing.T) {
	var plugin struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	readJSON(t, ".claude-plugin/plugin.json", &plugin)
	if plugin.Name != "refetch" || plugin.MCPServers["refetch"].Command != "${CLAUDE_PLUGIN_ROOT}/bin/refetch" {
		t.Errorf("plugin.json = %+v", plugin)
	}
	var hooks struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	readJSON(t, "hooks/hooks.json", &hooks)
	for _, ev := range []string{"SessionStart", "PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		groups := hooks.Hooks[ev]
		if len(groups) != 1 || groups[0].Hooks[0].Command != `"${CLAUDE_PLUGIN_ROOT}/bin/refetch" hook `+ev {
			t.Errorf("%s hooks = %+v", ev, groups)
		}
	}
	for _, ev := range []string{"PostToolUse", "PostToolUseFailure"} {
		m := hooks.Hooks[ev][0].Matcher
		for _, tool := range []string{"WebFetch", "mcp__plugin_context-mode_context-mode__ctx_fetch_and_index", "mcp__plugin_refetch_refetch__.*"} {
			if !strings.Contains(m, tool) {
				t.Errorf("%s matcher %q lacks %s", ev, m, tool)
			}
		}
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestShim(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("the shim supports macOS and Linux")
	}
	const version = "9.9.9"
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "refetch")
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", bin, "./cmd/refetch")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	arch := map[string]string{"arm64": "arm64", "amd64": "amd64"}[runtime.GOARCH]
	asset := fmt.Sprintf("refetch_%s_%s_%s.tar.gz", version, runtime.GOOS, arch)
	archive := tarGz(t, bin)
	sum := sha256.Sum256(archive)

	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v"+version+"/"+asset {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)

	// A plugin root with this version and the archive's checksum in the shim.
	root := filepath.Join(tmp, "plugin")
	shim := filepath.Join(root, "bin", "refetch")
	writePluginRoot(t, root, version, hex.EncodeToString(sum[:])+"  "+asset)

	run := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(shim, args...)
		cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + tmp, "XDG_CACHE_HOME=" + filepath.Join(tmp, "cache"),
			"REFETCH_RELEASE_URL=" + srv.URL}, env...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	if out, err := run(nil, "version"); err != nil || out != "refetch "+version {
		t.Fatalf("first run: %q, %v", out, err)
	}
	if out, err := run(nil, "version"); err != nil || out != "refetch "+version || downloads.Load() != 1 {
		t.Errorf("cached run: %q, %v, downloads %d", out, err, downloads.Load())
	}

	// REFETCH_BIN wins.
	fake := filepath.Join(tmp, "fake")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho fake \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := run([]string{"REFETCH_BIN=" + fake}, "version"); err != nil || out != "fake version" {
		t.Errorf("REFETCH_BIN: %q, %v", out, err)
	}

	// A Homebrew refetch with the exact version wins over the cache; a
	// relative directory is never tried.
	brew := filepath.Join(tmp, "brew")
	if err := os.MkdirAll(brew, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'refetch " + version + "'; else echo brew \"$@\"; fi\n"
	if err := os.WriteFile(filepath.Join(brew, "refetch"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := run([]string{"REFETCH_BREW_DIRS=" + brew}, "stats"); err != nil || out != "brew stats" {
		t.Errorf("Homebrew binary: %q, %v", out, err)
	}
	rel, err := filepath.Rel(tmp, brew)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shim, "version")
	cmd.Dir = tmp
	cmd.Env = []string{"PATH=" + rel + ":/usr/bin:/bin", "REFETCH_BREW_DIRS=" + rel, "HOME=" + tmp, "XDG_CACHE_HOME=" + filepath.Join(tmp, "cache")}
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "refetch "+version {
		t.Errorf("relative dirs must be skipped: %q, %v", out, err)
	}

	// A wrong checksum refuses the download; a hook call still exits 0.
	bad := filepath.Join(tmp, "bad")
	writePluginRoot(t, bad, version, strings.Repeat("0", 64)+"  "+asset)
	badShim := filepath.Join(bad, "bin", "refetch")
	cache2 := "XDG_CACHE_HOME=" + filepath.Join(tmp, "cache2")
	cmd = exec.Command(badShim, "version")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + tmp, cache2, "REFETCH_RELEASE_URL=" + srv.URL}
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "checksum mismatch") {
		t.Errorf("bad checksum: %q, %v", out, err)
	}
	cmd = exec.Command(badShim, "hook", "PostToolUse")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + tmp, cache2, "REFETCH_RELEASE_URL=" + srv.URL}
	if out, err := cmd.Output(); err != nil || len(out) != 0 {
		t.Errorf("hook with no binary: stdout %q, %v", out, err)
	}
}

// writePluginRoot copies the shim into root/bin with one sha256 line and
// writes a plugin.json with version.
func writePluginRoot(t *testing.T, root, version, shaLine string) {
	t.Helper()
	for _, d := range []string{"bin", ".claude-plugin"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shim, err := os.ReadFile("bin/refetch")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(shim), "# BEGIN SHA256\n", "# BEGIN SHA256\n# "+shaLine+"\n", 1)
	if err := os.WriteFile(filepath.Join(root, "bin", "refetch"), []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf("{\n  \"name\": \"refetch\",\n  \"version\": %q\n}\n", version)
	if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tarGz(t *testing.T, bin string) []byte {
	t.Helper()
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "refetch", Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
