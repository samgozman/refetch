package fetch

import (
	"bufio"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// upstream picks the HTTP proxy for a target, or nil to dial directly. It
// is unset unless UseProxy is called.
var upstream atomic.Pointer[func(*url.URL) (*url.URL, error)]

// proxyDialer reaches the proxy itself. The proxy is the user's own setting
// (often a corporate proxy, or one on 127.0.0.1), so it skips the
// public-only check that every target still gets.
var proxyDialer = net.Dialer{Timeout: 10 * time.Second}

// UseProxy sends every connection through the proxy in HTTPS_PROXY (or
// HTTP_PROXY) as a CONNECT tunnel, except to hosts NO_PROXY lists. TLS runs
// inside the tunnel, so the site still sees refetch's own handshake. It
// returns the proxy's address, or "" (and dials directly) when none is set.
func UseProxy() string {
	env := httpproxy.FromEnvironment()
	proxy := cmp.Or(env.HTTPSProxy, env.HTTPProxy)
	if proxy == "" {
		upstream.Store(nil)
		return ""
	}
	cfg := httpproxy.Config{HTTPSProxy: proxy, NoProxy: env.NoProxy}
	pick := cfg.ProxyFunc()
	upstream.Store(&pick)
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" {
		return proxy
	}
	return u.Scheme + "://" + u.Host
}

// proxyFor is the proxy for address (host:port); ok is false to dial
// directly.
func proxyFor(address string) (proxy *url.URL, ok bool, err error) {
	pick := upstream.Load()
	if pick == nil {
		return nil, false, nil
	}
	proxy, err = (*pick)(&url.URL{Scheme: "https", Host: address})
	return proxy, proxy != nil, err
}

func dialProxied(ctx context.Context, proxy *url.URL, address string) (net.Conn, error) {
	if proxy.Scheme != "http" {
		return nil, fmt.Errorf("proxy %s: only http:// proxies are supported", proxy.Host)
	}
	if err := checkTarget(ctx, address); err != nil {
		return nil, err
	}
	proxyAddr := proxy.Host
	if proxy.Port() == "" {
		proxyAddr = net.JoinHostPort(proxy.Hostname(), "80")
	}
	conn, err := proxyDialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy %s: %w", proxy.Host, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: http.Header{}}
	if u := proxy.User; u != nil {
		pass, _ := u.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pass)))
	}
	br := bufio.NewReader(conn)
	resp, err := connect(conn, br, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy %s: %w", proxy.Host, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("the proxy refused %s (%s)", address, resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

func connect(conn net.Conn, br *bufio.Reader, req *http.Request) (*http.Response, error) {
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	return http.ReadResponse(br, req)
}

// checkTarget is the public-only check for a connection the proxy makes:
// the proxy resolves the name, so only what can be checked here is.
// Literal addresses must be public, and a name that resolves locally must
// resolve only to public addresses. A name that does not resolve locally
// is left to the proxy.
func checkTarget(ctx context.Context, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return checkAddr(ip)
	}
	name := strings.TrimSuffix(strings.ToLower(host), ".")
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return fmt.Errorf("%s is not a public address", host)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil
	}
	var errs []error
	for _, ip := range ips {
		errs = append(errs, checkAddr(ip))
	}
	return errors.Join(errs...)
}

func checkAddr(ip netip.Addr) error {
	if !isPublic(ip) {
		return fmt.Errorf("%s is not a public address", ip)
	}
	if (!e2eLocal || !ip.IsLoopback()) && isLocal(ip) {
		return fmt.Errorf("%s is not a public address: it is this machine's own", ip)
	}
	return nil
}

// bufferedConn returns bytes the proxy sent right after its reply first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
