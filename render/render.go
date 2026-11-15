package render

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
	OutcomeSkippedRoot   = "skipped_root"    // running as root outside Linux, where Chrome's sandbox cannot run
)

// DefaultTimeout is render_timeout's default: Chrome start included.
const DefaultTimeout = 15 * time.Second

// Errors Render returns.
var (
	ErrNoChrome = errors.New("no Chrome or Chromium found")
	ErrRoot     = errors.New("running as root: headless Chrome needs its sandbox, which does not run as root (on Linux, refetch runs it as nobody instead)")
	ErrTimeout  = errors.New("headless Chrome timed out")
	ErrSandbox  = errors.New("headless Chrome cannot start its sandbox here: the container blocks the user namespaces it needs (Docker's default seccomp profile does)")
)

// Renderer runs the user's Chrome headless, behind a local proxy on the
// public-only dialer, and returns the DOM after the page's scripts ran.
type Renderer struct {
	Chrome  string        // binary; FindChrome() when empty
	Timeout time.Duration // hard cap, Chrome start included; DefaultTimeout when zero

	dial func(ctx context.Context, network, address string) (net.Conn, error) // tests replace it
}

// Render loads rawURL in a throwaway headless Chrome profile and returns
// the DOM after the page's scripts ran, read over the DevTools pipe. It
// opens http and https URLs only: Chrome would open file: ones too.
func (r *Renderer) Render(ctx context.Context, rawURL string) (Page, error) {
	if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Page{}, fmt.Errorf("not an http(s) URL: %q", rawURL)
	}
	chrome := cmp.Or(r.Chrome, FindChrome())
	if chrome == "" {
		return Page{}, ErrNoChrome
	}
	// Rendering untrusted pages without Chrome's sandbox would hand any
	// renderer exploit root, so Chrome never runs as root: on Linux it runs
	// as nobody instead, elsewhere the step does not run.
	asRoot := os.Geteuid() == 0
	if asRoot && !canDropRoot {
		return Page{}, ErrRoot
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
		return Page{}, err
	}
	defer px.close()
	sweepProfiles(time.Hour)
	profile, err := newProfile()
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = os.RemoveAll(profile) }()

	cmd, cmdW, replyR, stderr, err := startChrome(ctx, chrome, profile, px.addr, asRoot)
	if err != nil {
		return Page{}, err
	}
	defer func() { _, _ = cmdW.Close(), replyR.Close() }()
	t := &tab{c: newCDP(cmdW)}
	t.c.attached = t.attached
	t.c.start(replyR)
	page, err := t.drive(ctx, rawURL)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the browser and helpers that would outlive it
	_ = cmd.Wait()
	switch {
	case err == nil:
		return page, nil
	case errors.Is(err, errNotCommitted), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return Page{}, fmt.Errorf("%w after %s", ErrTimeout, timeout)
	case bytes.Contains(stderr.buf.Bytes(), []byte("No usable sandbox")):
		return Page{}, ErrSandbox
	}
	return Page{}, fmt.Errorf("headless Chrome failed: %w", err)
}

// startChrome starts Chrome on profile behind the proxy, and returns the
// pipe ends refetch writes commands to and reads replies from: Chrome
// reads fd 3 and writes fd 4.
func startChrome(ctx context.Context, chrome, profile, proxy string, asRoot bool) (cmd *exec.Cmd, cmdW, replyR *os.File, stderr *capped, err error) {
	cmdR, cmdW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	replyR, replyW, err := os.Pipe()
	if err != nil {
		_, _ = cmdR.Close(), cmdW.Close()
		return nil, nil, nil, nil, err
	}
	defer func() { _, _ = cmdR.Close(), replyW.Close() }() // Chrome's ends, which it holds once started
	fail := func(err error) (*exec.Cmd, *os.File, *os.File, *capped, error) {
		_, _ = cmdW.Close(), replyR.Close()
		return nil, nil, nil, nil, err
	}
	cmd = exec.CommandContext(ctx, chrome, chromeArgs(profile, proxy)...) //nolint:gosec // the user's own Chrome, found by FindChrome or REFETCH_CHROME
	cmd.ExtraFiles = []*os.File{cmdR, replyW}
	cmd.SysProcAttr = sysProcAttr()
	if asRoot {
		if err := runAsNobody(cmd, profile); err != nil {
			return fail(err)
		}
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // Chrome's helpers too
	cmd.WaitDelay = 2 * time.Second
	stderr = &capped{max: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fail(fmt.Errorf("headless Chrome failed: %w", err))
	}
	return cmd, cmdW, replyR, stderr, nil
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

func chromeArgs(profile, proxy string) []string {
	args := []string{
		"--headless", "--no-first-run", "--no-default-browser-check", "--disable-sync",
		"--disable-background-networking", "--mute-audio", "--hide-scrollbars",
		"--user-data-dir=" + profile, "--window-size=1280,800", "--screen-info=" + screenInfo(), "--accept-lang=en-US,en",
		// Every request through the proxy, loopback included (Chrome bypasses
		// a proxy for loopback unless told not to), and no QUIC or WebRTC
		// UDP, which would not go through it.
		"--proxy-server=http://" + proxy, "--proxy-bypass-list=<-loopback>", "--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		// navigator.webdriver stays false, as in a regular window; images
		// are never read, only waited for.
		"--disable-blink-features=AutomationControlled", "--blink-settings=imagesEnabled=false",
		"--remote-debugging-pipe",
	}
	if e2eLocal {
		args = append(args, "--ignore-certificate-errors")
	}
	return args
}

// screenInfo is a common laptop screen, in device pixels: headless Chrome's
// own is 800x600 with no menu bar, smaller than its window, which no real
// screen is.
func screenInfo() string {
	if runtime.GOOS == "darwin" {
		return "{2880x1800 devicePixelRatio=2 workAreaTop=50}"
	}
	return "{1920x1080 workAreaBottom=48}"
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
	cmd := exec.CommandContext(ctx, chrome, "--version")
	if os.Geteuid() == 0 { // as root, even this runs only as nobody, in a throwaway home
		home, err := os.MkdirTemp("", "refetch-version-")
		if err != nil {
			return ""
		}
		defer func() { _ = os.RemoveAll(home) }()
		cmd.SysProcAttr = &syscall.SysProcAttr{}
		if err := runAsNobody(cmd, home); err != nil {
			versions[chrome] = ""
			return ""
		}
	}
	out, err := cmd.Output()
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
