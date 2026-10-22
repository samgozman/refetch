package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// Chrome's own header values and order; a Chrome TLS handshake with Go's
// default headers is easy to spot, so they travel together.
var chromeHeaders = fhttp.Header{
	"sec-ch-ua":          {`"Google Chrome";v="150", "Chromium";v="150", "Not?A_Brand";v="24"`},
	"sec-ch-ua-mobile":   {"?0"},
	"sec-ch-ua-platform": {`"macOS"`},
	"user-agent":         {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"},
	"accept":             {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"},
	"accept-encoding":    {"gzip, deflate, br, zstd"},
	"accept-language":    {"en-US,en;q=0.9"},
	fhttp.HeaderOrderKey: {"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests", "user-agent", "accept", "accept-encoding", "accept-language"},
}

// chromeGet fetches with a Chrome 150 TLS and HTTP/2 fingerprint. It shares
// the public-only dialer and redirect rules, never uses a proxy or HTTP/3
// (QUIC would bypass the dialer), and keeps no cookies between fetches.
func chromeGet(ctx context.Context, rawURL string, timeout time.Duration) (int, []byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithClientProfile(profiles.Chrome_150),
		tlsclient.WithTimeoutSeconds(int(timeout.Seconds())),
		tlsclient.WithDialer(dialer),
		tlsclient.WithDisableHttp3(),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithCustomRedirectFunc(func(req *fhttp.Request, via []*fhttp.Request) error {
			return checkRedirect(req.URL, len(via))
		}),
	)
	if err != nil {
		return 0, nil, "", err
	}
	defer client.CloseIdleConnections()
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header = chromeHeaders.Clone()
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes))
	return resp.StatusCode, body, resp.Header.Get("Content-Type"), err
}

var plainClient = &http.Client{
	Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return checkRedirect(req.URL, len(via))
	},
}

func plainGet(ctx context.Context, rawURL, userAgent string, timeout time.Duration) (int, []byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := plainClient.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes))
	return resp.StatusCode, body, resp.Header.Get("Content-Type"), err
}

func checkRedirect(u *url.URL, hops int) error {
	if hops >= 5 {
		return errors.New("too many redirects")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("redirect to %s refused", u.Scheme)
	}
	return nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("not an http(s) URL: %q", raw)
	}
	return u, nil
}

func hostOf(raw string) string {
	if u, err := parseURL(raw); err == nil {
		return u.Hostname()
	}
	return ""
}
