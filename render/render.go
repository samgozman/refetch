package render

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/samgozman/refetch/fetch"
)

// Client is the attempt client name of the headless Chrome step.
const Client = "headless"

// Outcomes for a step 2 that was needed but did not run.
const (
	OutcomeSkippedOff    = "skipped_off"     // render = "off"
	OutcomeSkippedByCall = "skipped_by_call" // fetch_page(render: false)
	OutcomeNoChrome      = "no_chrome"       // no Chrome or Chromium found
	OutcomeSkippedRoot   = "skipped_root"    // running as root, where Chrome's sandbox cannot run
)

// DefaultTimeout is render_timeout's default: Chrome start included.
const DefaultTimeout = 15 * time.Second

// Errors Render returns.
var (
	ErrNoChrome = errors.New("no Chrome or Chromium found")
	ErrRoot     = errors.New("running as root: headless Chrome needs its sandbox, which does not run as root")
	ErrTimeout  = errors.New("headless Chrome timed out")
)

// Renderer runs the user's Chrome headless, behind a local proxy on the
// public-only dialer, and returns the DOM after the page's scripts ran.
type Renderer struct {
	Chrome  string        // binary; FindChrome() when empty
	Timeout time.Duration // hard cap, Chrome start included; DefaultTimeout when zero

	dial func(ctx context.Context, network, address string) (net.Conn, error) // tests replace it
}

// Render loads rawURL in a throwaway headless Chrome profile and returns the
// serialized DOM (at most fetch.MaxBytes).
func (r *Renderer) Render(ctx context.Context, rawURL string) ([]byte, error) {
	chrome := cmp.Or(r.Chrome, FindChrome())
	if chrome == "" {
		return nil, ErrNoChrome
	}
	// Rendering untrusted pages without Chrome's sandbox would hand any
	// renderer exploit root, so the step does not run as root at all.
	if os.Geteuid() == 0 {
		return nil, ErrRoot
	}
	timeout := cmp.Or(r.Timeout, DefaultTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dial := r.dial
	if dial == nil {
		dial = fetch.DialContext
	}
	px, err := startProxy(ctx, dial)
	if err != nil {
		return nil, err
	}
	defer px.close()
	sweepProfiles(time.Hour)
	profile, err := newProfile()
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(profile) }()

	cmd := exec.CommandContext(ctx, chrome, chromeArgs(ctx, chrome, profile, px.addr, rawURL, timeout)...) //nolint:gosec // the user's own Chrome, found by FindChrome or REFETCH_CHROME
	cmd.SysProcAttr = sysProcAttr()
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // Chrome's helpers too
	cmd.WaitDelay = 2 * time.Second
	out := &capped{max: fetch.MaxBytes}
	cmd.Stdout = out
	err = cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // helpers that outlived the browser
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w after %s", ErrTimeout, timeout)
	}
	if err != nil && out.buf.Len() == 0 {
		return nil, fmt.Errorf("headless Chrome failed: %w", err)
	}
	if out.buf.Len() == 0 {
		return nil, errors.New("headless Chrome returned no page")
	}
	return out.buf.Bytes(), nil
}

// profilePrefix names the throwaway profiles, so a later run can remove the
// ones a killed refetch left behind.
const profilePrefix = "refetch-chrome-"

// newProfile makes a throwaway Chrome profile whose downloads land inside
// it, so they are removed with it.
func newProfile() (string, error) {
	profile, err := os.MkdirTemp("", profilePrefix)
	if err != nil {
		return "", err
	}
	prefs, err := json.Marshal(map[string]any{
		"download":                   map[string]any{"default_directory": filepath.Join(profile, "downloads"), "prompt_for_download": false},
		"savefile":                   map[string]any{"default_directory": filepath.Join(profile, "downloads")},
		"credentials_enable_service": false,
	})
	if err == nil {
		err = os.MkdirAll(filepath.Join(profile, "Default"), 0o700)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(profile, "Default", "Preferences"), prefs, 0o600)
	}
	if err != nil {
		_ = os.RemoveAll(profile)
		return "", err
	}
	return profile, nil
}

// sweepProfiles removes throwaway profiles older than age: a refetch killed
// mid-render (a hook at its timeout) cannot remove its own.
func sweepProfiles(age time.Duration) {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), profilePrefix+"*"))
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && time.Since(fi.ModTime()) > age {
			_ = os.RemoveAll(d)
		}
	}
}

func chromeArgs(ctx context.Context, chrome, profile, proxy, rawURL string, timeout time.Duration) []string {
	budget := max(timeout-2*time.Second, time.Second)
	args := []string{
		"--headless", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--disable-sync", "--disable-background-networking",
		"--disable-component-update", "--disable-default-apps", "--mute-audio", "--hide-scrollbars",
		"--user-data-dir=" + profile,
		// Every request through the proxy, loopback included (Chrome bypasses
		// a proxy for loopback unless told not to), and no QUIC or WebRTC
		// UDP, which would not go through it.
		"--proxy-server=http://" + proxy, "--proxy-bypass-list=<-loopback>", "--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--user-agent=" + userAgent(ctx, chrome),
		"--virtual-time-budget=" + strconv.FormatInt(budget.Milliseconds(), 10),
		"--dump-dom",
	}
	if e2eLocal {
		args = append(args, "--ignore-certificate-errors")
	}
	return append(args, rawURL)
}

// capped keeps the first max bytes written and drops the rest.
type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

var chromeVersion = regexp.MustCompile(`(\d+)\.\d+\.\d+\.\d+`)

var (
	versionsMu sync.Mutex
	versions   = map[string]string{}
)

// Version is the installed Chrome's version ("155.0.8059.85"), or "" if it
// cannot be read within three seconds. It is cached per binary.
func Version(ctx context.Context, chrome string) string {
	versionsMu.Lock()
	defer versionsMu.Unlock()
	if v, ok := versions[chrome]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, chrome, "--version").Output()
	v := ""
	if err == nil {
		v = chromeVersion.FindString(string(out))
	}
	versions[chrome] = v
	return v
}

// Name describes the installed Chrome for the stats header, like "Google
// Chrome 155"; "" when not found.
func Name(ctx context.Context, chrome string) string {
	if chrome == "" {
		return ""
	}
	v := Version(ctx, chrome)
	name := "Chrome"
	if bytes.Contains(bytes.ToLower([]byte(chrome)), []byte("chromium")) {
		name = "Chromium"
	}
	if m := chromeVersion.FindStringSubmatch(v); m != nil {
		return name + " " + m[1]
	}
	return name
}

// userAgent is a regular Chrome User-Agent for the installed version.
// Headless Chrome's own says "HeadlessChrome", which many sites refuse.
func userAgent(ctx context.Context, chrome string) string {
	major := "150"
	if m := chromeVersion.FindStringSubmatch(Version(ctx, chrome)); m != nil {
		major = m[1]
	}
	platform := "Macintosh; Intel Mac OS X 10_15_7"
	if runtime.GOOS == "linux" {
		platform = "X11; Linux x86_64"
	}
	return "Mozilla/5.0 (" + platform + ") AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + major + ".0.0.0 Safari/537.36"
}
