package render

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// proxy is a local HTTP proxy that Chrome sends all its traffic through, so
// every connection Chrome makes goes through refetch's public-only dialer.
// Chrome's own network stack would otherwise reach loopback and private
// addresses.
type proxy struct {
	addr  string
	ln    net.Listener
	srv   *http.Server
	dial  func(ctx context.Context, network, address string) (net.Conn, error)
	plain *http.Transport
	wg    sync.WaitGroup

	// Tunnels are hijacked, so srv.Close does not end them; close does.
	mu      sync.Mutex
	closed  bool
	conns   map[net.Conn]struct{}
	tunnels sync.WaitGroup
}

func startProxy(ctx context.Context, dial func(ctx context.Context, network, address string) (net.Conn, error)) (*proxy, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &proxy{addr: ln.Addr().String(), ln: ln, dial: dial,
		plain: &http.Transport{Proxy: nil, DialContext: dial, MaxIdleConns: 4, IdleConnTimeout: 10 * time.Second}}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	p.wg.Go(func() { _ = p.srv.Serve(ln) })
	return p, nil
}

func (p *proxy) close() {
	_ = p.srv.Close()
	p.plain.CloseIdleConnections()
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.tunnels.Wait()
	p.wg.Wait()
}

// track registers a tunnel's connections, or closes them when the proxy is
// already closing.
func (p *proxy) track(conns ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		for _, c := range conns {
			_ = c.Close()
		}
		return false
	}
	if p.conns == nil {
		p.conns = map[net.Conn]struct{}{}
	}
	for _, c := range conns {
		p.conns[c] = struct{}{}
	}
	p.tunnels.Add(1)
	return true
}

func (p *proxy) untrack(conns ...net.Conn) {
	p.mu.Lock()
	for _, c := range conns {
		_ = c.Close()
		delete(p.conns, c)
	}
	p.mu.Unlock()
	p.tunnels.Done()
}

// ServeHTTP tunnels CONNECT (HTTPS) and forwards plain HTTP requests.
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	if r.URL.Host == "" {
		http.Error(w, "refetch proxy: absolute URL required", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	resp, err := p.plain.RoundTrip(out)
	if err != nil {
		http.Error(w, "refetch proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	dst, err := p.dial(ctx, "tcp", r.Host)
	cancel()
	if err != nil {
		http.Error(w, "refetch proxy: "+err.Error(), http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = dst.Close()
		http.Error(w, "refetch proxy: cannot tunnel", http.StatusInternalServerError)
		return
	}
	src, buf, err := hj.Hijack()
	if err != nil {
		_ = dst.Close()
		return
	}
	if !p.track(src, dst) {
		return
	}
	defer p.untrack(src, dst)
	if _, err := src.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if buf.Reader.Buffered() > 0 {
			_, _ = io.CopyN(dst, buf, int64(buf.Reader.Buffered()))
		}
		_, _ = io.Copy(dst, src)
		closeWrite(dst)
	})
	_, _ = io.Copy(src, dst)
	closeWrite(src)
	wg.Wait()
}

// closeWrite half-closes a connection so the other side sees EOF while
// replies still flow back.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
