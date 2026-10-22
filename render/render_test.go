package render

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
				setTimeout(function(){document.getElementById("root").innerHTML="<h1>Secret word: elder</h1>"}, 300)
				</script></body></html>`))
		case "/challenge":
			if c, err := r.Cookie("cleared"); err == nil && c.Value == "1" {
				_, _ = w.Write([]byte(`<html><body><h1>Secret word: damson</h1></body></html>`))
				return
			}
			_, _ = w.Write([]byte(`<html><head><title>Just a moment...</title></head><body>
				<script>document.cookie="cleared=1; path=/"; setTimeout(function(){location.reload()}, 200)</script>
				</body></html>`))
		case "/hang":
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func chromeOrSkip(t *testing.T) string {
	t.Helper()
	c := FindChrome()
	if c == "" {
		t.Skip("no Chrome or Chromium installed")
	}
	return c
}

// loopbackDial lets the test reach its own server; production dials with
// fetch.DialContext, which refuses loopback.
var loopbackDial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext

func TestRenderJavaScriptAndChallenge(t *testing.T) {
	chrome := chromeOrSkip(t)
	srv := site(t)
	r := &Renderer{Chrome: chrome, Timeout: 15 * time.Second, dial: loopbackDial}
	for path, want := range map[string]string{"/js": "Secret word: elder", "/challenge": "Secret word: damson"} {
		dom, err := r.Render(t.Context(), srv.URL+path)
		if err != nil || !strings.Contains(string(dom), want) {
			t.Errorf("%s: err %v, dom %.300q", path, err, dom)
		}
	}
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
	dom, _ := r.Render(t.Context(), srv.URL+"/js")
	if strings.Contains(string(dom), "Secret word") {
		t.Errorf("Chrome reached a loopback server around the proxy: %.300q", dom)
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
	// With a virtual time budget Chrome may give up first and dump an empty
	// page; either way it must end within the hard cap.
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("render took %s with a 3s timeout (err %v)", took, err)
	}
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
