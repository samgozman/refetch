// Package render is the headless Chrome step: it finds the user's installed
// Chrome or Chromium. refetch never downloads a browser.
package render

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
)

// macOSApps are the standard install paths, most common first.
var macOSApps = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
}

// pathNames are looked up on PATH after the macOS apps.
var pathNames = []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"}

// playwrightBuilds are Playwright's Chromium builds inside its browsers
// folder, the newest last when sorted.
var playwrightBuilds = []string{"chromium-*/chrome-linux64/chrome", "chromium-*/chrome-linux/chrome"}

// FindChrome returns the Chrome to run: REFETCH_CHROME if set, then the
// macOS app paths (also under ~/Applications), then PATH, then Playwright's
// Chromium. Empty means none.
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
	return playwrightChrome(home)
}

func playwrightChrome(home string) string {
	for _, p := range playwrightFound(home) {
		if ownedSafely(p) {
			return p
		}
	}
	return ""
}

// UnsafePlaywrightChrome is a Playwright Chromium that FindChrome passed
// over because others can write to it, as in Playwright's Docker images;
// "" when there is none.
func UnsafePlaywrightChrome() string {
	home, _ := os.UserHomeDir()
	for _, p := range playwrightFound(home) {
		if !ownedSafely(p) {
			return p
		}
	}
	return ""
}

// playwrightFound lists the Playwright Chromium builds, preferred first.
func playwrightFound(home string) []string {
	var all []string
	dirs := []string{"/ms-playwright", "/opt/pw-browsers", filepath.Join(home, ".cache", "ms-playwright")}
	if p := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); p != "" && p != "0" {
		dirs = append([]string{p}, dirs...)
	}
	for _, dir := range dirs {
		for _, pattern := range playwrightBuilds {
			found, _ := filepath.Glob(filepath.Join(dir, pattern))
			slices.SortFunc(found, func(a, b string) int { return cmp.Or(cmp.Compare(len(b), len(a)), cmp.Compare(b, a)) })
			for _, p := range found {
				if isExec(p) {
					all = append(all, p)
				}
			}
		}
	}
	return all
}

// ownedSafely reports that p and every folder above it belong to root or
// to this user and that nobody else can write to them, so a Chromium found
// by searching (not named by the user) was not planted by another account.
func ownedSafely(p string) bool {
	euid := uint32(os.Geteuid()) //nolint:gosec // a uid is never negative
	for {
		fi, err := os.Lstat(p)
		if err != nil {
			return false
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		// A sticky folder such as /tmp is fine above the build: others
		// cannot replace what they do not own there.
		sticky := fi.IsDir() && fi.Mode()&os.ModeSticky != 0
		if !ok || (st.Uid != 0 && st.Uid != euid) || (fi.Mode().Perm()&0o022 != 0 && !sticky) {
			return false
		}
		parent := filepath.Dir(p)
		if parent == p {
			return true
		}
		p = parent
	}
}

func isExec(p string) bool {
	fi, err := os.Stat(p) //nolint:gosec // p is the user's own REFETCH_CHROME or a fixed install path
	if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return false
	}
	return !canDropRoot || os.Geteuid() != 0 || nobodyCanRun(p)
}

// nobodyCanRun reports that any user can reach and run p: as root, Chrome
// runs as nobody, so one in a private folder such as /root/.cache would
// only fail later.
func nobodyCanRun(p string) bool {
	p, err := filepath.EvalSymlinks(p)
	for err == nil {
		fi, statErr := os.Stat(p) //nolint:gosec // p is a Chrome found above, or a folder above it
		if statErr != nil || fi.Mode().Perm()&0o001 == 0 {
			return false
		}
		parent := filepath.Dir(p)
		if parent == p {
			return true
		}
		p = parent
	}
	return false
}
