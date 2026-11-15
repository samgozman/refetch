package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
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

// reply is one HTTP response, cut at MaxBytes; mitigated is Cloudflare's
// cf-mitigated header, url the address after redirects.
type reply struct {
	status                      int
	body                        []byte
	contentType, mitigated, url string
}

// chromeGet fetches with a Chrome 150 TLS and HTTP/2 fingerprint. It shares
// the public-only dialer (and through it any proxy UseProxy set) and the
// redirect rules, never uses HTTP/3 (QUIC would bypass the dialer), and keeps
// no cookies between fetches.
func chromeGet(ctx context.Context, rawURL string, timeout time.Duration) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opts := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profiles.Chrome_150),
		tlsclient.WithTimeoutSeconds(int(timeout.Seconds())),
		tlsclient.WithDialContext(DialContext),
		tlsclient.WithDisableHttp3(),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithCustomRedirectFunc(func(req *fhttp.Request, via []*fhttp.Request) error {
			return checkRedirect(req.URL, len(via))
		}),
	}
	if e2eLocal {
		opts = append(opts, tlsclient.WithInsecureSkipVerify())
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), opts...)
	if err != nil {
		return reply{}, err
	}
	defer client.CloseIdleConnections()
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, rawURL, nil)
	if err != nil {
		return reply{}, err
	}
	req.Header = chromeHeaders.Clone()
	resp, err := client.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	ct := resp.Header.Get("Content-Type")
	body, err := readBody(resp.Body, ct)
	return reply{resp.StatusCode, body, ct, resp.Header.Get("Cf-Mitigated"), resp.Request.URL.String()}, err
}

// readBody reads a response body: a page up to MaxBytes, cut there; a
// response that may be a PDF or office file, by its type or its first
// bytes, up to MaxDocBytes, as a cut file cannot be read at all.
func readBody(r io.Reader, contentType string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxBytes))
	mt, _, _ := mime.ParseMediaType(contentType)
	if err != nil || len(body) < MaxBytes || !isDocType(mt) && !mayBeDocument(body) {
		return body, err
	}
	rest, err := io.ReadAll(io.LimitReader(r, MaxDocBytes+1-MaxBytes))
	body = append(body, rest...)
	if err == nil && len(body) > MaxDocBytes {
		return nil, fmt.Errorf("the file is larger than %d MB", MaxDocBytes>>20)
	}
	return body, err
}

var plainClient = &http.Client{
	Transport: &http.Transport{Proxy: nil, DialContext: DialContext, TLSClientConfig: &tls.Config{InsecureSkipVerify: e2eLocal}}, // InsecureSkipVerify is true only in the e2e build
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return checkRedirect(req.URL, len(via))
	},
}

func plainGet(ctx context.Context, rawURL, userAgent string, timeout time.Duration) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := plainClient.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	ct := resp.Header.Get("Content-Type")
	body, err := readBody(resp.Body, ct)
	return reply{resp.StatusCode, body, ct, resp.Header.Get("Cf-Mitigated"), resp.Request.URL.String()}, err
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

// IsWebURL reports whether raw is an http or https URL with a host, the
// only kind refetch reads.
func IsWebURL(raw string) bool {
	_, err := parseURL(raw)
	return err == nil
}

func hostOf(raw string) string {
	if u, err := parseURL(raw); err == nil {
		return u.Hostname()
	}
	return ""
}
