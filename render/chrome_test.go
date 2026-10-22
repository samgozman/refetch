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
