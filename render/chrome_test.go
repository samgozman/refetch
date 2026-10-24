package render

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindChrome(t *testing.T) {
	dir := t.TempDir()
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
	dir := t.TempDir()
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
}
