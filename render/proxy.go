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
	p.wg.Wait()
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
	if _, err := src.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = src.Close()
		_ = dst.Close()
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
	_ = src.Close()
	_ = dst.Close()
}

// closeWrite half-closes a connection so the other side sees EOF while
// replies still flow back.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
