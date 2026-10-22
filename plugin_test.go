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
	"slices"
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
	// Only the release job writes a version and checksums, on the release
	// branch; on main they stay empty, so a PR cannot ship either.
	shim, err := os.ReadFile("bin/refetch")
	if err != nil {
		t.Fatal(err)
	}
	if plugin.Version != "0.0.0" || !strings.Contains(string(shim), "# BEGIN SHA256\n# END SHA256\n") {
		t.Errorf("main must keep version 0.0.0 and an empty sha256 table, got %q", plugin.Version)
	}
	var market struct {
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Source string `json:"source"`
				Repo   string `json:"repo"`
				Ref    string `json:"ref"`
			} `json:"source"`
		} `json:"plugins"`
	}
	readJSON(t, ".claude-plugin/marketplace.json", &market)
	if len(market.Plugins) != 1 || market.Plugins[0].Source.Repo != "samgozman/refetch" || market.Plugins[0].Source.Ref != "release" {
		t.Errorf("marketplace.json must install from the release branch: %+v", market.Plugins)
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
	name := fmt.Sprintf("refetch_%s_%s_%s", version, runtime.GOOS, arch)
	archive := tarGz(t, bin)
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}

	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v"+version+"/"+name+".tar.gz" {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)

	// A plugin root with this version and the archive's and binary's
	// checksums in the shim.
	root := filepath.Join(tmp, "plugin")
	shim := filepath.Join(root, "bin", "refetch")
	writePluginRoot(t, root, version, sha(archive)+"  "+name+".tar.gz", sha(binary)+"  "+name)
	cacheDir := filepath.Join(tmp, "cache")
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + tmp, "XDG_CACHE_HOME=" + cacheDir, "REFETCH_RELEASE_URL=" + srv.URL}
	run := func(extra []string, args ...string) (string, error) {
		cmd := exec.Command(shim, args...)
		cmd.Env = append(slices.Clone(env), extra...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	if out, err := run(nil, "version"); err != nil || out != "refetch "+version {
		t.Fatalf("first run: %q, %v", out, err)
	}
	if out, err := run(nil, "version"); err != nil || out != "refetch "+version || downloads.Load() != 1 {
		t.Errorf("cached run: %q, %v, downloads %d", out, err, downloads.Load())
	}

	// A binary changed in the cache never runs: it is downloaded again.
	cached := filepath.Join(cacheDir, "refetch", version, "refetch")
	if err := os.WriteFile(cached, []byte("#!/bin/sh\necho tampered\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := run(nil, "version"); err != nil || out != "refetch "+version || downloads.Load() != 2 {
		t.Errorf("tampered cache: %q, %v, downloads %d", out, err, downloads.Load())
	}

	// REFETCH_BIN wins and gets the caller's PATH, not the shim's.
	fake := filepath.Join(tmp, "fake")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho fake \"$@\" \"$PATH\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := run([]string{"REFETCH_BIN=" + fake, "PATH=/opt/x:/usr/bin:/bin"}, "version"); err != nil || out != "fake version /opt/x:/usr/bin:/bin" {
		t.Errorf("REFETCH_BIN: %q, %v", out, err)
	}

	// The shim's own tools never come from a relative PATH entry.
	evil := filepath.Join(tmp, "evil")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"sed", "sha256sum", "openssl", "shasum", "tar", "curl"} {
		if err := os.WriteFile(filepath.Join(evil, tool), []byte("#!/bin/sh\necho evil-"+tool+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(shim, "version")
	cmd.Dir = tmp
	cmd.Env = []string{"PATH=evil:/usr/bin:/bin", "HOME=" + tmp, "XDG_CACHE_HOME=" + cacheDir}
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "refetch "+version {
		t.Errorf("relative PATH entries must not be used: %q, %v", out, err)
	}

	// A hook call exits 0 even when the binary fails.
	failing := filepath.Join(tmp, "failing")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := run([]string{"REFETCH_BIN=" + failing}, "hook", "PreToolUse"); err != nil {
		t.Errorf("hook with a failing binary: %v", err)
	}

	// A wrong checksum refuses the download; a hook call still exits 0
	// with no output.
	bad := filepath.Join(tmp, "bad")
	writePluginRoot(t, bad, version, strings.Repeat("0", 64)+"  "+name+".tar.gz", sha(binary)+"  "+name)
	badShim := filepath.Join(bad, "bin", "refetch")
	badEnv := []string{"PATH=/usr/bin:/bin", "HOME=" + tmp, "XDG_CACHE_HOME=" + filepath.Join(tmp, "cache2"), "REFETCH_RELEASE_URL=" + srv.URL}
	cmd = exec.Command(badShim, "version")
	cmd.Env = badEnv
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "checksum mismatch") {
		t.Errorf("bad checksum: %q, %v", out, err)
	}
	cmd = exec.Command(badShim, "hook", "PostToolUse")
	cmd.Env = badEnv
	if out, err := cmd.Output(); err != nil || len(out) != 0 {
		t.Errorf("hook with no binary: stdout %q, %v", out, err)
	}
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestPluginVersion runs the release job's script on copies of the real
// plugin.json and shim, with archives like GoReleaser's.
func TestPluginVersion(t *testing.T) {
	script, err := filepath.Abs("scripts/plugin-version.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, f := range []string{".claude-plugin/plugin.json", "bin/refetch"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"}
	// release writes one archive per platform (a binary that names its
	// platform) and checksums.txt, and returns the dir and the table the
	// shim should end up with.
	release := func(version string, platforms []string, corrupt bool) (string, string) {
		dir := t.TempDir()
		var sums, table strings.Builder
		for _, p := range platforms {
			binPath := filepath.Join(dir, "refetch")
			body := []byte("#!/bin/sh\necho " + p + "\n")
			if err := os.WriteFile(binPath, body, 0o755); err != nil {
				t.Fatal(err)
			}
			archive := tarGz(t, binPath)
			name := "refetch_" + version + "_" + p
			if err := os.WriteFile(filepath.Join(dir, name+".tar.gz"), archive, 0o644); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&sums, "%s  %s.tar.gz\n", sha(archive), name)
			fmt.Fprintf(&table, "# %s  %s.tar.gz\n# %s  %s\n", sha(archive), name, sha(body), name)
		}
		sums.WriteString(strings.Repeat("f", 64) + "  refetch_" + version + "_checksums.sig\n")
		if corrupt {
			sums.Reset()
			for _, p := range platforms {
				fmt.Fprintf(&sums, "%s  refetch_%s_%s.tar.gz\n", strings.Repeat("1", 64), version, p)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, table.String()
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(script, args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// A second release replaces the first one's table instead of adding to it.
	var want string
	for _, version := range []string{"0.1.0", "v0.2.0"} {
		dir, table := release(strings.TrimPrefix(version, "v"), all, false)
		if out, err := run(version, dir); err != nil {
			t.Fatalf("%s: %v\n%s", version, err, out)
		}
		want = table
	}
	var plugin struct {
		Version string `json:"version"`
	}
	readJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), &plugin)
	if plugin.Version != "0.2.0" {
		t.Errorf("plugin.json version = %q", plugin.Version)
	}
	shim, err := os.ReadFile(filepath.Join(root, "bin", "refetch"))
	if err != nil {
		t.Fatal(err)
	}
	_, table, _ := strings.Cut(string(shim), "# BEGIN SHA256\n")
	table, _, _ = strings.Cut(table, "# END SHA256\n")
	if table != want {
		t.Errorf("sha256 table:\n%s\nwant:\n%s", table, want)
	}
	if fi, err := os.Stat(filepath.Join(root, "bin", "refetch")); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("shim lost its exec bit: %v", err)
	}

	// A missing archive, an archive that does not match checksums.txt or a
	// bad version fails and changes nothing.
	dir, _ := release("0.3.0", all[:3], false)
	if out, err := run("0.3.0", dir); err == nil || !strings.Contains(out, "no sha256 for refetch_0.3.0_linux_arm64.tar.gz") {
		t.Errorf("missing archive: %v\n%s", err, out)
	}
	dir, _ = release("0.3.0", all, true)
	if out, err := run("0.3.0", dir); err == nil || !strings.Contains(out, "does not match checksums.txt") {
		t.Errorf("corrupt archive: %v\n%s", err, out)
	}
	if out, err := run("main", dir); err == nil || !strings.Contains(out, "not a release version") {
		t.Errorf("bad version: %v\n%s", err, out)
	}
	readJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), &plugin)
	if plugin.Version != "0.2.0" {
		t.Errorf("a failed run changed plugin.json to %q", plugin.Version)
	}
}

// writePluginRoot copies the shim into root/bin with the given sha256
// lines and writes a plugin.json with version.
func writePluginRoot(t *testing.T, root, version string, shaLines ...string) {
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
	var lines strings.Builder
	for _, l := range shaLines {
		lines.WriteString("# " + l + "\n")
	}
	s := strings.Replace(string(shim), "# BEGIN SHA256\n", "# BEGIN SHA256\n"+lines.String(), 1)
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
