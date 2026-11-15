package render

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
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

// drive loads rawURL in a new tab and reads its DOM: after the load event
// and once the text stops changing, or at the deadline, whatever the page
// holds by then. A page still loading at the deadline is read as it is.
// A challenge or empty page gets another look, in case it reloads itself.
func drive(ctx context.Context, c *cdp, rawURL, chrome string) (Page, error) {
	deadline, _ := ctx.Deadline()
	pageCtx, cancel := context.WithDeadline(ctx, deadline.Add(-snapshotReserve))
	defer cancel()
	s, err := openTab(pageCtx, c, chrome)
	if err != nil {
		return Page{}, err
	}
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := c.call(pageCtx, s, "Page.navigate", map[string]any{"url": rawURL}, &nav); err != nil {
		if pageCtx.Err() != nil {
			return Page{}, errNotCommitted
		}
		return Page{}, err
	}
	if nav.ErrorText != "" {
		return Page{}, fmt.Errorf("headless Chrome could not load the page: %s", nav.ErrorText)
	}
	wait, stop := pageCtx, context.CancelFunc(func() {})
	for {
		_, err := c.waitEvent(wait, s, "Page.loadEventFired")
		waited := wait.Err()
		stop()
		if err != nil && waited == nil {
			return Page{}, err
		}
		settle(pageCtx, c, s)
		p, err := snapshot(ctx, c, s)
		if err != nil || pageCtx.Err() != nil || !fetch.Challenge(http.StatusOK, p.DOM) && !fetch.EmptyRendered(p.DOM) {
			return p, err
		}
		wait, stop = context.WithTimeout(pageCtx, recheckWait)
	}
}

// openTab opens a blank tab, attached for commands, with trackers blocked
// and a regular Chrome's user agent and client hints; it returns the tab's
// session.
func openTab(ctx context.Context, c *cdp, chrome string) (string, error) {
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &target); err != nil {
		return "", err
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &att); err != nil {
		return "", err
	}
	s := att.SessionID
	for _, cmd := range []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Network.enable", nil},
		{"Network.setBlockedURLs", map[string]any{"urls": blockedURLs}},
		{"Emulation.setUserAgentOverride", uaOverride(ctx, chrome)},
	} {
		if err := c.call(ctx, s, cmd.method, cmd.params, nil); err != nil {
			return "", err
		}
	}
	return s, nil
}

// uaOverride is the user agent and client hints of the installed Chrome
// version, as a regular window sends them; headless Chrome's own name
// itself HeadlessChrome, which many sites refuse.
func uaOverride(ctx context.Context, chrome string) map[string]any {
	full := Version(ctx, chrome)
	major, _, _ := strings.Cut(full, ".")
	if major == "" {
		major, full = "150", "150.0.0.0"
	}
	brands := func(v string) []map[string]string {
		return []map[string]string{{"brand": "Google Chrome", "version": v}, {"brand": "Chromium", "version": v}, {"brand": "Not?A_Brand", "version": "24"}}
	}
	platform, navPlatform := "macOS", "MacIntel"
	if runtime.GOOS == "linux" {
		platform, navPlatform = "Linux", "Linux x86_64"
	}
	arch := "x86"
	if runtime.GOARCH == "arm64" {
		arch = "arm"
	}
	return map[string]any{
		"userAgent": userAgent(ctx, chrome), "acceptLanguage": "en-US,en;q=0.9", "platform": navPlatform,
		"userAgentMetadata": map[string]any{
			"brands": brands(major), "fullVersionList": brands(full), "fullVersion": full,
			"platform": platform, "platformVersion": "", "architecture": arch, "model": "", "mobile": false,
		},
	}
}

// settle waits until the page's text length holds still across two
// measurements, for settleMax at most.
func settle(ctx context.Context, c *cdp, s string) {
	ctx, cancel := context.WithTimeout(ctx, settleMax)
	defer cancel()
	last := -1
	tick := time.NewTicker(settlePoll)
	defer tick.Stop()
	for {
		var n int
		if err := evaluate(ctx, c, s, "document.body ? document.body.innerText.length : 0", &n); err != nil {
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

// snapshotJS reads the DOM, cut in the page so that a huge one never
// crosses the pipe, and the address the page ended on.
var snapshotJS = fmt.Sprintf(`(() => { const d = document.documentElement; const h = d ? d.outerHTML : "";
	return {url: location.href, html: h.length > %d ? h.slice(0, %d) : h}; })()`, fetch.MaxBytes, fetch.MaxBytes)

func snapshot(ctx context.Context, c *cdp, s string) (Page, error) {
	var v struct {
		URL  string `json:"url"`
		HTML string `json:"html"`
	}
	if err := evaluate(ctx, c, s, snapshotJS, &v); err != nil {
		return Page{}, err
	}
	dom := []byte(v.HTML)
	return Page{DOM: dom[:min(len(dom), fetch.MaxBytes)], URL: v.URL}, nil
}

// evaluate runs a JavaScript expression in the page and decodes its value
// into out.
func evaluate(ctx context.Context, c *cdp, s, expr string, out any) error {
	var res struct {
		Result struct {
			Value jsontext.Value `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := c.call(ctx, s, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		return fmt.Errorf("page script: %s", res.ExceptionDetails.Text)
	}
	return json.Unmarshal(res.Result.Value, out)
}
