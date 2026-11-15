package render

import (
	"context"
	"encoding/json/v2"
	"errors"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// site serves a page that only JavaScript fills in, and a challenge that a
// browser clears by setting a cookie and reloading.
func site(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/js":
			_, _ = w.Write([]byte(`<html><body><div id="root"></div><script>
				setTimeout(function(){document.getElementById("root").innerHTML="<h1>Secret word: elder</h1><p>From the orchard notes.</p>"}, 300)
				</script></body></html>`))
		case "/challenge":
			if c, err := r.Cookie("cleared"); err == nil && c.Value == "1" {
				_, _ = w.Write([]byte(`<html><body><h1>Secret word: damson</h1><p>From the orchard notes.</p></body></html>`))
				return
			}
			_, _ = w.Write([]byte(`<html><head><title>Just a moment...</title></head><body>
				<script>document.cookie="cleared=1; path=/"; setTimeout(function(){location.reload()}, 200)</script>
				</body></html>`))
		case "/loading": // an iframe that never loads holds back the load event
			_, _ = w.Write([]byte(`<html><body><h1>Secret word: fig</h1><p>From the orchard notes.</p><iframe src="/hang"></iframe></body></html>`))
		case "/tracked":
			_, _ = w.Write([]byte(`<html><body><h1>Secret word: plum</h1><p id="ua"></p><script src="/tracker.js"></script>
				<script>document.getElementById("ua").textContent = "webdriver=" + navigator.webdriver</script></body></html>`))
		case "/component": // the text lives in a web component's shadow root
			_, _ = w.Write([]byte(`<html><body><h1>Case</h1><case-view></case-view><script>
				customElements.define("case-view", class extends HTMLElement { connectedCallback() {
					this.attachShadow({mode: "open"}).innerHTML = "<p>Secret word: quince, from the orchard notes.</p>"; } });
				</script></body></html>`))
		case "/fingerprint":
			_, _ = w.Write([]byte(fingerprintPage))
		case "/tracker.js":
			tracked.Add(1)
		case "/hang":
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fingerprintPage writes what a bot-detection script would read, and
// marks the body if anything calls innerText or outerHTML from the page's
// own world, as a script driving the page there would.
const fingerprintPage = `<html><body><h1>Fingerprint</h1><p id="fp">pending</p><script>
for (const [proto, prop] of [[HTMLElement.prototype, "innerText"], [Element.prototype, "outerHTML"]]) {
	const d = Object.getOwnPropertyDescriptor(proto, prop);
	Object.defineProperty(proto, prop, {get() { document.body.setAttribute("data-hooked", prop); return d.get.call(this); }, configurable: true});
}
(async () => {
	const h = await navigator.userAgentData.getHighEntropyValues(["platformVersion", "fullVersionList"]);
	const w = new Worker(URL.createObjectURL(new Blob(["postMessage(navigator.userAgent)"])));
	const worker = await new Promise(res => { w.onmessage = e => res(e.data); setTimeout(() => res("no answer"), 3000); });
	document.getElementById("fp").textContent = JSON.stringify({ua: navigator.userAgent, worker,
		brands: navigator.userAgentData.brands.map(b => b.brand).join("|"), pv: h.platformVersion, fvl: h.fullVersionList.length,
		screen: screen.width + "x" + screen.height, webdriver: navigator.webdriver}) + " end of fingerprint, padded to be read as a page";
})();
</script></body></html>`

// tracked counts requests for /tracker.js, which a render must block.
var tracked atomic.Int32

func chromeOrSkip(t *testing.T) string {
	t.Helper()
	c := FindChrome()
	if c == "" {
		t.Skip("no Chrome or Chromium installed")
	}
	return c
}

// loopbackDial lets the test reach its own server and nothing else, so the
// tests never touch the network (Chrome's own background requests to Google
// are refused too). Production dials with fetch.DialContext, which refuses
// loopback.
func loopbackDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		outside.Add(1)
		return nil, errors.New(address + " is outside the test")
	}
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
}

// outside counts dials the tests refused because they left the machine.
var outside atomic.Int32

func TestRenderJavaScriptAndChallenge(t *testing.T) {
	chrome := chromeOrSkip(t)
	srv := site(t)
	r := &Renderer{Chrome: chrome, Timeout: 15 * time.Second, dial: loopbackDial}
	for path, want := range map[string]string{"/js": "Secret word: elder", "/challenge": "Secret word: damson"} {
		p, err := r.Render(t.Context(), srv.URL+path)
		if err != nil || !strings.Contains(string(p.DOM), want) || p.URL != srv.URL+path {
			t.Errorf("%s: err %v, url %q, dom %.300q", path, err, p.URL, p.DOM)
		}
	}
	t.Logf("refused %d dials outside the test machine", outside.Load())
}

func TestRenderGoesThroughThePublicOnlyDialer(t *testing.T) {
	chrome := chromeOrSkip(t)
	srv := site(t)
	var dials atomic.Int32
	refuse := func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("127.0.0.1 is not a public address")
	}
	r := &Renderer{Chrome: chrome, Timeout: 10 * time.Second, dial: refuse}
	p, _ := r.Render(t.Context(), srv.URL+"/js")
	if strings.Contains(string(p.DOM), "Secret word") {
		t.Errorf("Chrome reached a loopback server around the proxy: %.300q", p.DOM)
	}
	if dials.Load() == 0 {
		t.Error("Chrome did not use the proxy")
	}
}

func TestRenderTimeout(t *testing.T) {
	chrome := chromeOrSkip(t)
	srv := site(t)
	r := &Renderer{Chrome: chrome, Timeout: 3 * time.Second, dial: loopbackDial}
	start := time.Now()
	_, err := r.Render(t.Context(), srv.URL+"/hang")
	if took := time.Since(start); took > 6*time.Second || !errors.Is(err, ErrTimeout) {
		t.Errorf("render took %s with a 3s timeout, err %v", took, err)
	}
}

func TestRenderReadsAPageStillLoading(t *testing.T) {
	chrome := chromeOrSkip(t)
	srv := site(t)
	r := &Renderer{Chrome: chrome, Timeout: 5 * time.Second, dial: loopbackDial}
	start := time.Now()
	p, err := r.Render(t.Context(), srv.URL+"/loading")
	if err != nil || !strings.Contains(string(p.DOM), "Secret word: fig") {
		t.Errorf("err %v, dom %.300q", err, p.DOM)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("render took %s with a 5s timeout", took)
	}
}

func TestRenderBlocksTrackersAndLooksRegular(t *testing.T) {
	chrome := chromeOrSkip(t)
	var ua, hints atomic.Value
	srv := site(t)
	seen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/tracked" {
			ua.Store(req.Header.Get("User-Agent"))
			hints.Store(req.Header.Get("Sec-Ch-Ua"))
		}
		srv.Config.Handler.ServeHTTP(w, req)
	}))
	t.Cleanup(seen.Close)
	old := blockedURLs
	blockedURLs = append(slices.Clone(old), "*/tracker.js*")
	t.Cleanup(func() { blockedURLs = old })
	r := &Renderer{Chrome: chrome, Timeout: 10 * time.Second, dial: loopbackDial}
	p, err := r.Render(t.Context(), seen.URL+"/tracked")
	if err != nil || !strings.Contains(string(p.DOM), "webdriver=false") {
		t.Errorf("err %v, dom %.300q", err, p.DOM)
	}
	if n := tracked.Load(); n != 0 {
		t.Errorf("the tracker was fetched %d times", n)
	}
	if u, _ := ua.Load().(string); strings.Contains(u, "Headless") || !strings.Contains(u, "Chrome/") {
		t.Errorf("User-Agent %q", u)
	}
	if h, _ := hints.Load().(string); strings.Contains(h, "Headless") {
		t.Errorf("Sec-Ch-Ua %q", h)
	}
}

func TestRenderRefusesLocalURLs(t *testing.T) {
	r := &Renderer{Chrome: filepath.Join(t.TempDir(), "never-runs")}
	for _, u := range []string{"file:///etc/passwd", "file://app.example.com/etc/passwd", "chrome://settings", "javascript:alert(1)", "https://"} {
		if _, err := r.Render(t.Context(), u); err == nil || !strings.Contains(err.Error(), "not an http(s) URL") {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestRenderFingerprint(t *testing.T) {
	chrome := chromeOrSkip(t)
	srv := site(t)
	r := &Renderer{Chrome: chrome, Timeout: 10 * time.Second, dial: loopbackDial}
	p, err := r.Render(t.Context(), srv.URL+"/fingerprint")
	dom := string(p.DOM)
	start := strings.Index(dom, `{"ua"`)
	end := strings.Index(dom, " end of fingerprint")
	if err != nil || start < 0 || end < start {
		t.Fatalf("err %v, dom %.500q", err, dom)
	}
	var fp struct {
		UA        string `json:"ua"`
		Worker    string `json:"worker"`
		Brands    string `json:"brands"`
		PV        string `json:"pv"`
		Screen    string `json:"screen"`
		FVL       int    `json:"fvl"`
		Webdriver bool   `json:"webdriver"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(dom[start:end])), &fp); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fp.UA, "Headless") || strings.Contains(fp.Worker, "Headless") || !strings.Contains(fp.Worker, "Chrome/") {
		t.Errorf("user agents: page %q, worker %q", fp.UA, fp.Worker)
	}
	if strings.Contains(fp.Brands, "Headless") || !strings.Contains(fp.Brands, "Chromium") || fp.FVL == 0 || fp.Webdriver {
		t.Errorf("client hints: %+v", fp)
	}
	if fp.Screen == "800x600" {
		t.Errorf("headless Chrome's own screen size: %s", fp.Screen)
	}
	if i := strings.Index(dom, `data-hooked="`); i >= 0 {
		t.Errorf("a page script saw refetch read the page: %.100q", dom[i:])
	}
	t.Logf("%+v", fp)
}

func TestNoChrome(t *testing.T) {
	r := &Renderer{Chrome: filepath.Join(t.TempDir(), "missing")}
	if _, err := r.Render(t.Context(), "https://example.com/"); err == nil {
		t.Error("a missing Chrome binary must fail")
	}
	t.Setenv("REFETCH_CHROME", filepath.Join(t.TempDir(), "missing"))
	if _, err := (&Renderer{}).Render(t.Context(), "https://example.com/"); !errors.Is(err, ErrNoChrome) {
		t.Errorf("err = %v, want ErrNoChrome", err)
	}
}

func TestCapped(t *testing.T) {
	c := &capped{max: 5}
	_, _ = c.Write([]byte("abc"))
	_, _ = c.Write([]byte("defgh"))
	if c.buf.String() != "abcde" {
		t.Errorf("capped = %q", c.buf.String())
	}
}

func TestNoUsableSandbox(t *testing.T) {
	chrome := filepath.Join(chromeDir(t), "chrome")
	script := "#!/bin/sh\necho 'FATAL:zygote_host_impl_linux.cc(128)] No usable sandbox!' >&2\nexit 134\n"
	if err := os.WriteFile(chrome, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Renderer{Chrome: chrome, Timeout: 5 * time.Second, dial: loopbackDial}
	if _, err := r.Render(t.Context(), "https://example.com/"); !errors.Is(err, ErrSandbox) {
		t.Errorf("Chrome without a sandbox: %v", err)
	}
}

func TestRenderReadsShadowRoots(t *testing.T) {
	chrome := chromeOrSkip(t)
	srv := site(t)
	r := &Renderer{Chrome: chrome, Timeout: 10 * time.Second, dial: loopbackDial}
	p, err := r.Render(t.Context(), srv.URL+"/component")
	if err != nil || !strings.Contains(string(p.DOM), `<template shadowrootmode="open"><p>Secret word: quince`) {
		t.Errorf("err %v, dom %.400q", err, p.DOM)
	}
}

func TestThreeParts(t *testing.T) {
	for in, want := range map[string]string{"15.6": "15.6.0", "27.2.0": "27.2.0", "6.8.0-45-generic": "6.8.0", "6.1.12.4": "6.1.12", "": "", "x": ""} {
		if got := threeParts(in); got != want {
			t.Errorf("threeParts(%q) = %q, want %q", in, got, want)
		}
	}
}
