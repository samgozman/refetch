package render

import (
	"os"
	"path/filepath"
	"testing"
)

// chromeDir is a temporary folder for fake Chromes. As root, refetch runs
// Chrome as nobody, who must be able to reach it.
func chromeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if os.Geteuid() == 0 {
		for _, d := range []string{dir, filepath.Dir(dir)} {
			if err := os.Chmod(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

func TestFindChrome(t *testing.T) {
	dir := chromeDir(t)
	bin := filepath.Join(dir, "chromium")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REFETCH_CHROME", bin)
	if got := FindChrome(); got != bin {
		t.Errorf("REFETCH_CHROME: %q", got)
	}
	t.Setenv("REFETCH_CHROME", filepath.Join(dir, "missing"))
	if got := FindChrome(); got != "" {
		t.Errorf("a missing REFETCH_CHROME must not fall back: %q", got)
	}
	t.Setenv("REFETCH_CHROME", "")
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	if got := FindChrome(); got != bin && !isExec(got) {
		t.Errorf("PATH lookup: %q", got)
	}
}

func TestPlaywrightChrome(t *testing.T) {
	dir := chromeDir(t)
	for _, p := range []string{"chromium-999/chrome-linux/chrome", "chromium-1200/chrome-linux64/chrome", "chromium_headless_shell-1300/chrome-linux/headless_shell"} {
		bin := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", dir)
	if got, want := playwrightChrome(t.TempDir()), filepath.Join(dir, "chromium-1200", "chrome-linux64", "chrome"); got != want {
		t.Errorf("playwrightChrome = %q, want the newest full Chromium %q", got, want)
	}
}

func TestPlaywrightChromeOthersCanWrite(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chromium-1200", "chrome-linux64", "chrome")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "chromium-1200"), 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", dir)
	if got := playwrightChrome(t.TempDir()); got == bin {
		t.Errorf("a Chromium in a folder others can write was used: %s", got)
	}
	if got := UnsafePlaywrightChrome(); got != bin {
		t.Errorf("UnsafePlaywrightChrome = %q, want %q", got, bin)
	}
}

func TestNobodyCanRun(t *testing.T) {
	// /tmp and everything above it are open to everyone; only this folder
	// is changed.
	dir, err := os.MkdirTemp("/tmp", "refetch-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	bin := filepath.Join(dir, "chrome")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if nobodyCanRun(bin) {
		t.Errorf("%s is inside a private folder", bin)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !nobodyCanRun(bin) {
		t.Errorf("%s is open to everyone", bin)
	}
	if nobodyCanRun(filepath.Join(dir, "missing")) {
		t.Error("a missing file can be run")
	}
}
