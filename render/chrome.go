// Package render is the headless Chrome step: it finds the user's installed
// Chrome or Chromium. refetch never downloads a browser.
package render

import (
	"os"
	"os/exec"
)

// macOSApps are the standard install paths, most common first.
var macOSApps = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
}

// pathNames are looked up on PATH after the macOS apps.
var pathNames = []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"}

// FindChrome returns the Chrome to run: REFETCH_CHROME if set, then the
// macOS app paths (also under ~/Applications), then PATH. Empty means none.
func FindChrome() string {
	if p := os.Getenv("REFETCH_CHROME"); p != "" {
		if isExec(p) {
			return p
		}
		return ""
	}
	home, _ := os.UserHomeDir()
	for _, p := range macOSApps {
		for _, c := range []string{p, home + p} {
			if isExec(c) {
				return c
			}
		}
	}
	for _, n := range pathNames {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

func isExec(p string) bool {
	fi, err := os.Stat(p) //nolint:gosec // p is the user's own REFETCH_CHROME or a fixed install path
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}
