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
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// chromeMajor is the Chrome version the Chrome-like client is: its TLS
// profile, User-Agent and client hints all name it, as a mismatch between
// them is what bot managers look for.
const chromeMajor = 152

var chromeProfile = profiles.Chrome_152

// chromeHeaders are Chrome's own navigation headers, values and order, as
// a real Chrome sends them for an address typed in its bar (checked against
// Chrome 157; image/jxl joined Accept in Chrome 155).
var chromeHeaders = fhttp.Header{
	"sec-ch-ua":                 {secCHUA(chromeMajor)},
	"sec-ch-ua-mobile":          {"?0"},
	"sec-ch-ua-platform":        {`"macOS"`},
	"upgrade-insecure-requests": {"1"},
	"user-agent":                {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + strconv.Itoa(chromeMajor) + ".0.0.0 Safari/537.36"},
	"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
	"sec-fetch-site":            {"none"},
	"sec-fetch-mode":            {"navigate"},
	"sec-fetch-user":            {"?1"},
	"sec-fetch-dest":            {"document"},
	"accept-encoding":           {"gzip, deflate, br, zstd"},
	"accept-language":           {"en-US,en;q=0.9"},
	"priority":                  {"u=0, i"},
	fhttp.HeaderOrderKey: {"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests", "user-agent", "accept",
		"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest", "accept-encoding", "accept-language", "priority"},
}

// secCHUA is the Sec-CH-UA header Chrome of a major version sends.
func secCHUA(major int) string {
	var parts []string
	for _, b := range ChromeBrands(major) {
		parts = append(parts, `"`+b[0]+`";v="`+b[1]+`"`)
	}
	return strings.Join(parts, ", ")
}

// ChromeBrands are the brands and major versions Chrome of a major version
// names in its client hints, in its order. Chromium derives the GREASE
// brand, the brand's version and the order of the three from the major
// version, so a list from another version gives a client away.
func ChromeBrands(major int) [3][2]string {
	chars := []string{" ", "(", ":", "-", ".", "/", ")", ";", "=", "?", "_"}
	orders := [6][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	v := strconv.Itoa(major)
	brands := [3][2]string{
		{"Not" + chars[major%len(chars)] + "A" + chars[(major+1)%len(chars)] + "Brand", []string{"8", "99", "24"}[major%3]},
		{"Chromium", v},
		{"Google Chrome", v},
	}
	var out [3][2]string
	for i, at := range orders[major%6] {
		out[at] = brands[i]
	}
	return out
}

// reply is one HTTP response, cut at MaxBytes; mitigated is Cloudflare's
// cf-mitigated header, url the address after redirects.
type reply struct {
	status                      int
	body                        []byte
	contentType, mitigated, url string
}

// chromeGet fetches with a Chrome TLS and HTTP/2 fingerprint, extensions
// shuffled per connection as Chrome does. It shares
// the public-only dialer (and through it any proxy UseProxy set) and the
// redirect rules, never uses HTTP/3 (QUIC would bypass the dialer), and keeps
// no cookies between fetches.
func chromeGet(ctx context.Context, rawURL string, timeout time.Duration) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opts := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(chromeProfile),
		tlsclient.WithRandomTLSExtensionOrder(),
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
