package render

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startTestProxy(t *testing.T, dial func(ctx context.Context, network, address string) (net.Conn, error)) *proxy {
	t.Helper()
	p, err := startProxy(t.Context(), dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	return p
}

func TestProxyTunnelsAndForwards(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Proxy-Connection", r.Header.Get("Proxy-Connection"))
		_, _ = io.WriteString(w, "hello from "+r.URL.Path)
	})
	tlsSite := httptest.NewTLSServer(handler)
	t.Cleanup(tlsSite.Close)
	plainSite := httptest.NewServer(handler)
	t.Cleanup(plainSite.Close)
	p := startTestProxy(t, loopbackDial)
	proxyURL, err := url.Parse("http://" + p.addr)
	if err != nil {
		t.Fatal(err)
	}
	tr := tlsSite.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)

	for _, site := range []string{tlsSite.URL + "/tls", plainSite.URL + "/plain"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, site, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Proxy-Connection", "keep-alive")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", site, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		// Hop-by-hop headers are dropped on plain HTTP; a tunnel cannot see them.
		stripped := resp.Header.Get("X-Seen-Proxy-Connection") == ""
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "hello from /") || stripped != strings.HasSuffix(site, "/plain") {
			t.Errorf("%s: %d %q %v", site, resp.StatusCode, body, resp.Header)
		}
	}
}

func TestProxyRefuses(t *testing.T) {
	refuse := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("10.0.0.1 is not a public address")
	}
	p := startTestProxy(t, refuse)
	send := func(raw string) string {
		t.Helper()
		c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", p.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		if _, err := io.WriteString(c, raw); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.Status + " " + string(body)
	}
	if got := send("CONNECT 10.0.0.1:443 HTTP/1.1\r\nHost: 10.0.0.1:443\r\n\r\n"); !strings.HasPrefix(got, "403") || !strings.Contains(got, "not a public address") {
		t.Errorf("CONNECT to a private address: %q", got)
	}
	if got := send("GET http://10.0.0.1/ HTTP/1.1\r\nHost: 10.0.0.1\r\n\r\n"); !strings.HasPrefix(got, "502") || !strings.Contains(got, "not a public address") {
		t.Errorf("GET to a private address: %q", got)
	}
	if got := send("GET /relative HTTP/1.1\r\nHost: example.com\r\n\r\n"); !strings.HasPrefix(got, "400") {
		t.Errorf("relative URL: %q", got)
	}
}

func TestName(t *testing.T) {
	if got := Name(t.Context(), ""); got != "" {
		t.Errorf("no Chrome: %q", got)
	}
	dir := t.TempDir()
	for file, want := range map[string]string{"chromium": "Chromium 151", "google-chrome": "Chrome 150", "broken-chrome": "Chrome"} {
		script := "#!/bin/sh\necho 'Google Chrome 150.0.7000.1'\n"
		switch file {
		case "chromium":
			script = "#!/bin/sh\necho 'Chromium 151.0.7100.2'\n"
		case "broken-chrome":
			script = "#!/bin/sh\nexit 1\n"
		}
		path := filepath.Join(dir, file)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := Name(t.Context(), path); got != want {
			t.Errorf("Name(%s) = %q, want %q", file, got, want)
		}
	}
}

// TestProxyCloseEndsTunnels: a server that accepts and never answers must
// not pin tunnels (and their goroutines) after the render ends.
func TestProxyCloseEndsTunnels(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c) // never read, never closed
		}
	}()
	p, err := startProxy(t.Context(), loopbackDial)
	if err != nil {
		t.Fatal(err)
	}
	var clients []net.Conn
	for range 5 {
		c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", p.addr)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
		if _, err := io.WriteString(c, "CONNECT "+ln.Addr().String()+" HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT: %s", resp.Status)
		}
	}
	done := make(chan struct{})
	go func() { p.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not end the open tunnels")
	}
	for _, c := range clients {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Error("a tunnel is still open after close")
		}
		_ = c.Close()
	}
}

func TestSweepProfiles(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	old, err := newProfile()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := newProfile()
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(fresh, "Default", "Preferences")); err != nil || !strings.Contains(string(b), filepath.Join(fresh, "downloads")) {
		t.Errorf("profile preferences %q, %v", b, err)
	}
	long := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, long, long); err != nil {
		t.Fatal(err)
	}
	sweepProfiles(time.Hour)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("stale profile kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh profile removed: %v", err)
	}
}
