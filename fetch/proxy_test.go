package fetch

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// connectProxy is a loopback CONNECT proxy that tunnels every target to
// backend, so tests reach a local server under a name that does not
// resolve, the way a cloud session's proxy resolves names itself.
type connectProxy struct {
	mu      sync.Mutex
	targets []string
	auth    []string
	status  int
	backend string
}

func (p *connectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.targets = append(p.targets, r.Host)
	p.auth = append(p.auth, r.Header.Get("Proxy-Authorization"))
	status := p.status
	p.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	dst, err := net.Dial("tcp", p.backend)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	src, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	_, _ = src.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() { _, _ = io.Copy(dst, src); _ = dst.Close() }()
	_, _ = io.Copy(src, dst)
	_ = src.Close()
}

// withProxy starts a backend and a proxy in front of it and routes this
// test's connections through the proxy.
func withProxy(t *testing.T, user *url.Userinfo) *connectProxy {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><body>through the proxy</body></html>")
	}))
	t.Cleanup(backend.Close)
	cp := &connectProxy{backend: backend.Listener.Addr().String()}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)
	proxy := &url.URL{Scheme: "http", Host: srv.Listener.Addr().String(), User: user}
	pick := func(*url.URL) (*url.URL, error) { return proxy, nil } //nolint:unparam // the shape httpproxy's ProxyFunc has
	upstream.Store(&pick)
	t.Cleanup(func() { upstream.Store(nil) })
	return cp
}

func TestProxyTunnelsBothClients(t *testing.T) {
	cp := withProxy(t, url.UserPassword("agent", "s3cret"))
	for name, get := range map[string]func() (int, []byte, error){
		"plain": func() (int, []byte, error) {
			s, b, _, err := plainGet(t.Context(), "http://refetch-test.invalid/page", "refetch-test", 5*time.Second)
			return s, b, err
		},
		"chrome_like": func() (int, []byte, error) {
			s, b, _, err := chromeGet(t.Context(), "http://refetch-test.invalid/page", 5*time.Second)
			return s, b, err
		},
	} {
		status, body, err := get()
		if err != nil || status != http.StatusOK || !strings.Contains(string(body), "through the proxy") {
			t.Errorf("%s through the proxy: %d %q %v", name, status, body, err)
		}
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.targets) == 0 || cp.targets[0] != "refetch-test.invalid:80" {
		t.Errorf("CONNECT targets %v", cp.targets)
	}
	if cp.auth[0] != "Basic YWdlbnQ6czNjcmV0" {
		t.Errorf("Proxy-Authorization %q", cp.auth[0])
	}
}

func TestProxyStillRefusesNonPublicTargets(t *testing.T) {
	cp := withProxy(t, nil)
	for _, addr := range []string{"127.0.0.1:80", "10.1.2.3:443", "[::1]:443", "localhost:80", "app.localhost:443"} {
		if _, err := DialContext(t.Context(), "tcp", addr); err == nil || !strings.Contains(err.Error(), "not a public address") {
			t.Errorf("dial %s through the proxy: %v", addr, err)
		}
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.targets) != 0 {
		t.Errorf("the proxy was asked for %v", cp.targets)
	}
}

func TestProxyRefusalIsReported(t *testing.T) {
	cp := withProxy(t, nil)
	cp.status = http.StatusForbidden
	_, err := DialContext(t.Context(), "tcp", "refetch-test.invalid:443")
	if err == nil || !strings.Contains(err.Error(), "the proxy refused refetch-test.invalid:443 (403 Forbidden)") {
		t.Errorf("refused CONNECT: %v", err)
	}
}

func TestUseProxy(t *testing.T) {
	t.Cleanup(func() { upstream.Store(nil) })
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	if got := UseProxy(); got != "" || upstream.Load() != nil {
		t.Fatalf("no proxy set: %q", got)
	}
	t.Setenv("HTTPS_PROXY", "http://user:pw@127.0.0.1:3128")
	t.Setenv("NO_PROXY", "registry.example.org,10.0.0.0/8")
	if got := UseProxy(); got != "http://127.0.0.1:3128" {
		t.Errorf("UseProxy = %q, want the address without credentials", got)
	}
	for addr, proxied := range map[string]bool{"www.example.com:443": true, "registry.example.org:443": false, "10.0.0.5:443": false} {
		p, ok, err := proxyFor(addr)
		if err != nil || ok != proxied {
			t.Errorf("proxyFor(%s) = %v, %v, %v; want proxied %v", addr, p, ok, err, proxied)
		}
	}
	if _, err := DialContext(t.Context(), "tcp", "10.0.0.5:443"); err == nil {
		t.Error("a NO_PROXY private address was dialled directly")
	}
}

func TestOnlyHTTPProxies(t *testing.T) {
	_, err := dialProxied(t.Context(), &url.URL{Scheme: "socks5", Host: "127.0.0.1:1080"}, "example.com:443")
	if err == nil || !strings.Contains(err.Error(), "only http:// proxies") {
		t.Errorf("socks5 proxy: %v", err)
	}
}
