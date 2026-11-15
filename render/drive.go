package render

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samgozman/refetch/fetch"
)

// Page is what headless Chrome read: the DOM after the page's scripts ran
// (at most fetch.MaxBytes), and the address the page ended on.
type Page struct {
	DOM []byte
	URL string
}

// Timing of a render within its timeout.
const (
	snapshotReserve = 2 * time.Second        // kept back to read the DOM at the deadline
	settlePoll      = 250 * time.Millisecond // how often the text is measured after load
	settleMax       = 3 * time.Second        // the longest wait for the text to stop changing
	recheckWait     = time.Second            // after a challenge or empty page, the wait for a reload
)

// blockedURLs are ad and analytics hosts: a page reads the same without
// them and loads sooner.
var blockedURLs = []string{
	"*://*doubleclick.net/*", "*://*googlesyndication.com/*", "*://*googleadservices.com/*",
	"*://*google-analytics.com/*", "*://*googletagmanager.com/*", "*://*facebook.net/*",
	"*://*amazon-adsystem.com/*", "*://*adnxs.com/*", "*://*criteo.com/*", "*://*criteo.net/*",
	"*://*taboola.com/*", "*://*outbrain.com/*", "*://*scorecardresearch.com/*", "*://*quantserve.com/*",
	"*://*hotjar.com/*", "*://*clarity.ms/*", "*://*segment.io/*", "*://*mixpanel.com/*", "*://*nr-data.net/*",
}

// errNotCommitted is a navigation that got no answer before the deadline.
var errNotCommitted = errors.New("the page did not answer")

// tab is one page being read: its DevTools session, the user agent its
// frames and workers get, and its main frame for the isolated world.
type tab struct {
	c       *cdp
	session string
	frame   string

	mu       sync.Mutex
	override map[string]any // the user agent and client hints every target gets

	altLanguage bool // Accept-Language from the override, not --accept-lang
}

// drive loads rawURL in a new tab and reads its DOM: after the load event,
// a few scrolls for content that loads into view, and once the text stops
// changing, or at the deadline, whatever the page holds by then. A page
// still loading at the deadline is read as it is. A challenge or empty
// page gets another look, in case it reloads itself. A captcha wall, which
// never clears by itself, gets one more try in a new tab whose request
// headers differ in where Accept-Language sits (see userAgent).
func (t *tab) drive(ctx context.Context, rawURL string) (Page, error) {
	deadline, _ := ctx.Deadline()
	pageCtx, cancel := context.WithDeadline(ctx, deadline.Add(-snapshotReserve))
	defer cancel()
	p, err := t.load(ctx, pageCtx, rawURL)
	if err == nil && captchaWall(p.DOM) && time.Until(deadline) > retryNeeds {
		t.altLanguage = true
		if p2, err := t.load(ctx, pageCtx, rawURL); err == nil && !captchaWall(p2.DOM) {
			return p2, nil
		}
	}
	return p, err
}

// retryNeeds is the time left that a second try at a captcha wall needs.
const retryNeeds = 6 * time.Second

// captchaWall reports a page that asks a person to solve a captcha:
// DataDome's, PerimeterX's, Reddit's. Unlike Cloudflare's interstitial,
// waiting never clears it.
func captchaWall(dom []byte) bool {
	lower := bytes.ToLower(dom[:min(len(dom), 256<<10)])
	for _, m := range []string{"captcha-delivery.com", "px-captcha", "prove your humanity"} {
		if bytes.Contains(lower, []byte(m)) {
			return true
		}
	}
	return false
}

// load opens a tab and reads rawURL in it, within pageCtx; the DOM is read
// within ctx, which allows snapshotReserve more.
func (t *tab) load(ctx, pageCtx context.Context, rawURL string) (Page, error) {
	if err := t.open(pageCtx); err != nil {
		return Page{}, err
	}
	var nav struct {
		FrameID   string `json:"frameId"`
		ErrorText string `json:"errorText"`
	}
	if err := t.c.call(pageCtx, t.session, "Page.navigate", map[string]any{"url": rawURL}, &nav); err != nil {
		if pageCtx.Err() != nil {
			return Page{}, errNotCommitted
		}
		return Page{}, err
	}
	if nav.ErrorText != "" {
		return Page{}, fmt.Errorf("headless Chrome could not load the page: %s", nav.ErrorText)
	}
	t.frame = nav.FrameID
	wait, stop := pageCtx, context.CancelFunc(func() {})
	for round := 0; ; round++ {
		_, err := t.c.waitEvent(wait, t.session, "Page.loadEventFired")
		waited := wait.Err()
		stop()
		if err != nil && waited == nil {
			return Page{}, err
		}
		if round == 0 {
			var done bool
			_ = t.evaluate(pageCtx, scrollJS, &done)
		}
		t.settle(pageCtx)
		p, err := t.snapshot(ctx)
		if err != nil || pageCtx.Err() != nil || captchaWall(p.DOM) || !fetch.Challenge(http.StatusOK, p.DOM) && !fetch.EmptyRendered(p.DOM) {
			return p, err
		}
		wait, stop = context.WithTimeout(pageCtx, recheckWait)
	}
}

// open opens a blank tab, attached for commands, with trackers blocked, a
// regular Chrome's user agent on the tab and on every frame and worker it
// starts, and those paused until they have it.
func (t *tab) open(ctx context.Context) error {
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := t.c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return err
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := t.c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &att); err != nil {
		return err
	}
	t.session = att.SessionID
	override, err := t.userAgent(ctx)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.override = override
	t.mu.Unlock()
	return t.prepare(ctx, t.session, "page")
}

// prepare readies a page, frame or worker session: trackers blocked, the
// user agent set, its own frames and workers held until they are ready.
func (t *tab) prepare(ctx context.Context, session, kind string) error {
	t.mu.Lock()
	override := t.override
	t.mu.Unlock()
	cmds := []struct {
		method string
		params any
	}{
		{"Network.enable", nil},
		{"Network.setBlockedURLs", map[string]any{"urls": blockedURLs}},
		{"Network.setUserAgentOverride", override},
	}
	if kind != "worker" {
		cmds = append(cmds, []struct {
			method string
			params any
		}{
			{"Page.enable", nil},
			{"Emulation.setUserAgentOverride", override},
			{"Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}},
		}...)
	}
	for _, cmd := range cmds {
		if err := t.c.call(ctx, session, cmd.method, cmd.params, nil); err != nil && kind == "page" {
			return err
		}
	}
	return nil
}

// attached readies a frame or worker the page started, which Chrome holds
// until it is resumed, and always resumes it.
func (t *tab) attached(params jsontext.Value) {
	var ev struct {
		SessionID  string `json:"sessionId"`
		TargetInfo struct {
			Type string `json:"type"`
		} `json:"targetInfo"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.SessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	switch ev.TargetInfo.Type {
	case "iframe":
		_ = t.prepare(ctx, ev.SessionID, "frame")
	case "worker", "shared_worker":
		_ = t.prepare(ctx, ev.SessionID, "worker")
	}
	_ = t.c.call(ctx, ev.SessionID, "Runtime.runIfWaitingForDebugger", nil, nil)
}

// userAgent is the installed Chrome's user agent with "HeadlessChrome"
// named "Chrome", and the client hints a regular window of that version
// sends on this machine. They cannot be read from the page: a blank tab is
// not a secure context, so it has no navigator.userAgentData.
func (t *tab) userAgent(ctx context.Context) (map[string]any, error) {
	var v struct {
		Product   string `json:"product"`
		UserAgent string `json:"userAgent"`
	}
	if err := t.c.call(ctx, "", "Browser.getVersion", nil, &v); err != nil {
		return nil, err
	}
	_, full, _ := strings.Cut(v.Product, "/")
	major, _ := strconv.Atoi(strings.Split(full, ".")[0])
	if major == 0 {
		return map[string]any{"userAgent": strings.ReplaceAll(v.UserAgent, "HeadlessChrome", "Chrome")}, nil
	}
	var brands, fullBrands []map[string]string
	for _, b := range fetch.ChromeBrands(major) {
		brands = append(brands, map[string]string{"brand": b[0], "version": b[1]})
		version := full
		if b[1] != strconv.Itoa(major) {
			version = b[1] + ".0.0.0"
		}
		fullBrands = append(fullBrands, map[string]string{"brand": b[0], "version": version})
	}
	platform, navPlatform, arch := "Linux", "Linux x86_64", "x86"
	if runtime.GOARCH == "arm64" {
		navPlatform, arch = "Linux aarch64", "arm"
	}
	if runtime.GOOS == "darwin" {
		platform, navPlatform = "macOS", "MacIntel"
	}
	out := map[string]any{
		"userAgent": strings.ReplaceAll(v.UserAgent, "HeadlessChrome", "Chrome"), "platform": navPlatform,
		"userAgentMetadata": map[string]any{
			"brands": brands, "fullVersionList": fullBrands, "fullVersion": full, "platform": platform,
			"platformVersion": osVersion(), "architecture": arch, "bitness": "64", "model": "", "mobile": false,
			"wow64": false, "formFactors": []string{"Desktop"},
		},
	}
	// Accept-Language comes from --accept-lang, in Chrome's own header
	// order; set here (a plain list, Chrome adds the q-values) it moves up
	// beside the user agent. Measured in Nov 2026, G2's DataDome served
	// only the first and Reddit only the second, so a captcha wall gets a
	// second try with the other.
	if t.altLanguage {
		out["acceptLanguage"] = "en-US,en"
	}
	return out, nil
}

// threeParts pads a dotted version to three numbers ("15.6" is "15.6.0")
// and drops what follows them ("6.8.0-45-generic" is "6.8.0").
func threeParts(v string) string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r != '.' && (r < '0' || r > '9') })
	if len(parts) == 0 {
		return ""
	}
	nums := strings.Split(parts[0], ".")
	for len(nums) < 3 {
		nums = append(nums, "0")
	}
	return strings.Join(nums[:3], ".")
}

// scrollJS scrolls down a viewport at a time, six at most, for content
// that loads as it comes into view, then back to the top.
const scrollJS = `(async () => {
	for (let i = 0; i < 6; i++) {
		const s = document.scrollingElement;
		if (!s || s.scrollTop + innerHeight >= s.scrollHeight - 2) break;
		scrollBy(0, innerHeight);
		await new Promise(r => setTimeout(r, 250));
	}
	scrollTo(0, 0);
	return true;
})()`

// settle waits until the page's text length holds still across two
// measurements, for settleMax at most.
func (t *tab) settle(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, settleMax)
	defer cancel()
	last := -1
	tick := time.NewTicker(settlePoll)
	defer tick.Stop()
	for {
		var n int
		if err := t.evaluate(ctx, "document.body ? document.body.innerText.length : 0", &n); err != nil {
			return
		}
		if n > 0 && n == last {
			return
		}
		last = n
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// snapshotJS reads the DOM with the content of open shadow roots (web
// components keep a page's text there), cut in the page so that a huge one
// never crosses the pipe, and the address the page ended on.
var snapshotJS = fmt.Sprintf(`(() => {
	const roots = [];
	const walk = (root) => {
		const w = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
		for (let n = w.currentNode; n && roots.length < 5000; n = w.nextNode()) {
			if (n.shadowRoot) { roots.push(n.shadowRoot); walk(n.shadowRoot); }
		}
	};
	walk(document);
	const d = document.documentElement;
	let h = "";
	if (d) h = roots.length && d.getHTML ? "<html>" + d.getHTML({shadowRoots: roots}) + "</html>" : d.outerHTML;
	return {url: location.href, html: h.length > %d ? h.slice(0, %d) : h};
})()`, fetch.MaxBytes, fetch.MaxBytes)

func (t *tab) snapshot(ctx context.Context) (Page, error) {
	var v struct {
		URL  string `json:"url"`
		HTML string `json:"html"`
	}
	if err := t.evaluate(ctx, snapshotJS, &v); err != nil {
		return Page{}, err
	}
	dom := []byte(v.HTML)
	return Page{DOM: dom[:min(len(dom), fetch.MaxBytes)], URL: v.URL}, nil
}

// evaluate runs a JavaScript expression in an isolated world of the main
// frame, which shares the page's DOM but none of its scripts, so a page
// that wraps DOM methods to spot automation sees nothing; before the first
// navigation, in the blank tab itself. Promises are awaited.
func (t *tab) evaluate(ctx context.Context, expr string, out any) error {
	params := map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true}
	if t.frame != "" {
		var world struct {
			ID int64 `json:"executionContextId"`
		}
		err := t.c.call(ctx, t.session, "Page.createIsolatedWorld", map[string]any{"frameId": t.frame, "worldName": "refetch"}, &world)
		if err != nil {
			return err
		}
		params["contextId"] = world.ID
	}
	var res struct {
		Result struct {
			Value jsontext.Value `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := t.c.call(ctx, t.session, "Runtime.evaluate", params, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		return fmt.Errorf("page script: %s", res.ExceptionDetails.Text)
	}
	return json.Unmarshal(res.Result.Value, out)
}
