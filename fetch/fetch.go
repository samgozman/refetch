// Package fetch reads one public web page per call: public addresses only, no
// crawling, no cookies. By default a client with a Chrome TLS fingerprint goes
// first, with a plain Go client as backup for its library's own TLS or
// protocol errors; robots.txt is consulted only when asked.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/temoto/robotstxt"
)

// Clients, the steps of the ladder that produced an attempt.
const (
	ClientChromeLike  = "chrome_like"
	ClientPlain       = "plain"        // the plain client as the first step (Impersonate off or fallback)
	ClientPlainBackup = "plain_backup" // the plain client after the Chrome-like client errored
)

// Outcomes of one attempt.
const (
	OutcomeOK         = "ok"
	OutcomeRefused    = "refused"    // HTTP 403 or 429
	OutcomeChallenge  = "challenge"  // HTTP 503 or a bot-challenge page, even on a 403 (Cloudflare's is)
	OutcomeNeedsJS    = "needs_js"   // an HTML shell whose content only JavaScript would render
	OutcomeTimeout    = "timeout"    // the client's time budget ran out
	OutcomeError      = "error"      // network error, other HTTP status, address refusal
	OutcomeDisallowed = "disallowed" // robots.txt, only with RespectRobots
)

// Impersonation modes.
const (
	ImpersonateAlways   = "always"   // Chrome-like first, plain backup on its TLS or protocol errors (default)
	ImpersonateFallback = "fallback" // plain first, Chrome-like when the plain client is refused or challenged
	ImpersonateOff      = "off"      // plain client only
)

// Defaults for the zero Fetcher.
const (
	DefaultUserAgent   = "refetch"
	DefaultRobotsAgent = "refetch"
	DefaultTimeout     = 20 * time.Second
	MaxBytes           = 2 << 20
	robotsTTL          = time.Hour
)

// Attempt is one client's try at a page, reported to Fetcher.Record.
type Attempt struct {
	URL, Host, Client, Outcome string
	Status                     int
	Bytes                      int
	Duration                   time.Duration
	Err                        string
}

// Page is a fetched page, or the attempts that failed to fetch it.
type Page struct {
	Body        []byte
	ContentType string
	URL         string    // where Body came from, after redirects; "" when unknown
	Last        Attempt   // the attempt that produced Body, or the last failure
	Attempts    []Attempt // every attempt of this call, in order
}

// Fetcher gets pages. The zero value impersonates Chrome first and ignores
// robots.txt.
type Fetcher struct {
	Impersonate   string        // ImpersonateAlways when empty
	RespectRobots bool          // consult robots.txt before fetching
	UserAgent     string        // the plain client's User-Agent; DefaultUserAgent when empty
	RobotsAgent   string        // the robots.txt agent name; DefaultRobotsAgent when empty
	Timeout       time.Duration // per client; DefaultTimeout when zero
	Record        func(Attempt) // called after every attempt, if set

	plain, chrome getter // tests replace them
	mu            sync.Mutex
	robots        map[string]robotsEntry
}

type getter func(ctx context.Context, url string) (reply, error)

type robotsEntry struct {
	data    *robotstxt.RobotsData // nil = allow all
	fetched time.Time
}

// blockedError is a refusal or challenge: the case where the fallback mode
// tries the Chrome-like client.
type blockedError string

func (e blockedError) Error() string { return string(e) }

// Get fetches rawURL, walking the clients the impersonation mode allows. The
// error names why the page could not be read.
func (f *Fetcher) Get(ctx context.Context, rawURL string) (Page, error) {
	if _, err := parseURL(rawURL); err != nil {
		return Page{}, err
	}
	var p Page
	if f.RespectRobots && !f.allowed(ctx, rawURL) {
		a := Attempt{URL: rawURL, Client: ClientPlain, Outcome: OutcomeDisallowed, Err: "disallowed by robots.txt"}
		f.record(&p, &a)
		return p, errors.New("disallowed by robots.txt")
	}
	var err error
	switch f.Impersonate {
	case ImpersonateOff:
		err = f.try(ctx, &p, ClientPlain, rawURL)
	case ImpersonateFallback:
		if err = f.try(ctx, &p, ClientPlain, rawURL); isBlocked(err) {
			err = f.try(ctx, &p, ClientChromeLike, rawURL)
		}
	default:
		err = f.try(ctx, &p, ClientChromeLike, rawURL)
		if err != nil && p.Last.Status == 0 && backupWorthy(err) && ctx.Err() == nil {
			err = f.try(ctx, &p, ClientPlainBackup, rawURL)
		}
	}
	return p, err
}

// try runs one client, appends its attempt to p and, on success, sets the
// body.
func (f *Fetcher) try(ctx context.Context, p *Page, client, rawURL string) error {
	get := f.getter(client)
	start := time.Now()
	r, err := get(ctx, rawURL)
	status, body, ct := r.status, r.body, r.contentType
	a := Attempt{URL: rawURL, Client: client, Status: status, Bytes: len(body), Duration: time.Since(start), Outcome: OutcomeOK}
	switch {
	case err != nil:
		a.Outcome = OutcomeError
		if isTimeout(err) {
			a.Outcome = OutcomeTimeout
		}
		a.Err = err.Error()
	case strings.EqualFold(r.mitigated, "challenge") || Challenge(status, body):
		a.Outcome = OutcomeChallenge
		err = blockedError(fmt.Sprintf("challenge page (HTTP %d, %s client)", status, client))
	case status == http.StatusForbidden || status == http.StatusTooManyRequests:
		a.Outcome = OutcomeRefused
		err = blockedError(fmt.Sprintf("refused (HTTP %d, %s client)", status, client))
	case status != http.StatusOK:
		a.Outcome = OutcomeError
		err = fmt.Errorf("HTTP %d (%s client)", status, client)
	case NotText(ct, body) != "":
		a.Outcome = OutcomeError
		err = fmt.Errorf("%w (%s)", ErrNotText, NotText(ct, body))
	case NeedsJS(body, ct):
		a.Outcome = OutcomeNeedsJS
		err = fmt.Errorf("page needs JavaScript to show its content (HTTP %d, %s client)", status, client)
	}
	if err != nil && a.Err == "" {
		a.Err = err.Error()
	}
	f.record(p, &a)
	if err == nil {
		p.Body, p.ContentType, p.URL = body, ct, r.url
	}
	return err
}

func (f *Fetcher) getter(client string) getter {
	if client == ClientChromeLike {
		if f.chrome != nil {
			return f.chrome
		}
		return func(ctx context.Context, u string) (reply, error) {
			return chromeGet(ctx, u, f.timeout())
		}
	}
	if f.plain != nil {
		return f.plain
	}
	return func(ctx context.Context, u string) (reply, error) {
		return plainGet(ctx, u, f.userAgent(), f.timeout())
	}
}

func (f *Fetcher) record(p *Page, a *Attempt) {
	a.Host = hostOf(a.URL)
	p.Last = *a
	p.Attempts = append(p.Attempts, *a)
	if f.Record != nil {
		f.Record(*a)
	}
}

func (f *Fetcher) timeout() time.Duration {
	if f.Timeout > 0 {
		return f.Timeout
	}
	return DefaultTimeout
}

func (f *Fetcher) userAgent() string {
	if f.UserAgent != "" {
		return f.UserAgent
	}
	return DefaultUserAgent
}

// backupWorthy reports whether a Chrome-like client error is a TLS or HTTP/2
// quirk of its library; timeouts, DNS failures and address refusals are not.
func backupWorthy(err error) bool {
	if isTimeout(err) {
		return false
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"not a public address", "no such host", "connection refused", "not an http(s) url", "redirect"} {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

func isBlocked(err error) bool {
	_, ok := errors.AsType[blockedError](err)
	return ok
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}

// challengeMarkers appear in bot-protection interstitials (Cloudflare,
// DataDome, PerimeterX, Imperva, Akamai).
var challengeMarkers = []string{
	"challenge-platform", "cf-browser-verification", "cf_chl_", "<title>just a moment...</title>",
	"attention required! | cloudflare", "captcha-delivery.com", "px-captcha", "_incapsula_resource",
	"/_sec/cp_challenge", "enable javascript and cookies to continue",
	"prove your humanity", // Reddit's verification page, also after JavaScript ran
}

// Challenge reports whether a response is a bot challenge rather than the
// page: HTTP 503, HTTP 202 (AWS WAF answers a GET with an empty 202 and a
// challenge script), or a short page carrying a challenge marker.
func Challenge(status int, body []byte) bool {
	if status == http.StatusServiceUnavailable || status == http.StatusAccepted {
		return true
	}
	if len(body) > 256<<10 {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, m := range challengeMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// mountPoint is an empty element a JavaScript framework renders into.
var mountPoint = regexp.MustCompile(`(?i)<div[^>]+id=["']?(root|app|__next|__nuxt|svelte|main)["']?[^>]*>\s*</div>|<app-root[^>]*>\s*</app-root>`)

// jsNotices are what a page says when it cannot work without JavaScript.
var jsNotices = []string{
	"enable javascript", "javascript is required", "requires javascript", "javascript is disabled",
	"javascript must be enabled", "turn on javascript",
}

// NeedsJS reports whether an HTML page is a JavaScript-only shell: under
// about 200 characters of readable text plus an empty mount point or an
// enable-JavaScript notice, or under 100 characters plus a script (an app
// shell, or a proof-of-work page that solves a puzzle and resubmits).
func NeedsJS(body []byte, contentType string) bool {
	if len(body) > 512<<10 || (contentType != "" && !strings.Contains(contentType, "html")) {
		return false
	}
	text := len([]rune(Text(body, "text/html")))
	if text >= 200 {
		return false
	}
	if mountPoint.Match(body) || (text < 100 && bytes.Contains(bytes.ToLower(body), []byte("<script"))) {
		return true
	}
	lower := strings.ToLower(string(body))
	for _, n := range jsNotices {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// EmptyRendered reports whether a DOM from headless Chrome has next to no
// text. Unlike NeedsJS it ignores scripts and mount points, which stay.
func EmptyRendered(dom []byte) bool {
	return len([]rune(strings.TrimSpace(Text(dom, "text/html")))) < 20
}

// Blocked reports an anti-bot refusal: HTTP 403 or 429, or a challenge.
func Blocked(status int, body []byte) bool {
	return status == http.StatusForbidden || status == http.StatusTooManyRequests || Challenge(status, body)
}

// allowed checks the host's robots.txt for the robots agent, cached for an
// hour. Per the robots spec, 4xx means allow all and 5xx disallow all; a
// network error allows (the page fetch will fail on its own).
func (f *Fetcher) allowed(ctx context.Context, rawURL string) bool {
	u, err := parseURL(rawURL)
	if err != nil {
		return true
	}
	origin := u.Scheme + "://" + u.Host
	f.mu.Lock()
	e, ok := f.robots[origin]
	f.mu.Unlock()
	if !ok || time.Since(e.fetched) > robotsTTL {
		r, err := f.getter(ClientPlain)(ctx, origin+"/robots.txt")
		e = robotsEntry{fetched: time.Now()}
		if err == nil {
			if data, perr := robotstxt.FromStatusAndBytes(r.status, r.body); perr == nil {
				e.data = data
			}
		}
		f.mu.Lock()
		if f.robots == nil {
			f.robots = map[string]robotsEntry{}
		}
		f.robots[origin] = e
		f.mu.Unlock()
	}
	agent := f.RobotsAgent
	if agent == "" {
		agent = DefaultRobotsAgent
	}
	return e.data == nil || e.data.TestAgent(u.RequestURI(), agent)
}

// dialer refuses non-public addresses after DNS resolution, for both clients.
var dialer = net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}

// DialContext dials the way both clients do: public addresses only, checked
// after DNS resolution, or through the proxy UseProxy set. The headless
// Chrome step's proxy dials with it.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	proxy, ok, err := proxyFor(address)
	if err != nil {
		return nil, err
	}
	if ok {
		return dialProxied(ctx, proxy, address)
	}
	return dialer.DialContext(ctx, network, address)
}

// nonPublic are the IANA special-purpose ranges that are not globally
// reachable, the IPv6 forms whose embedded IPv4 address cannot be checked
// (IPv4-compatible, Teredo, local-use NAT64), and Azure's WireServer, a
// public address only reachable from inside Azure.
var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "168.63.129.16/32",
		"::/96", "::1/128", "100::/64", "2001::/32", "2001:2::/48", "2001:db8::/32", "3fff::/20",
		"5f00::/16", "64:ff9b:1::/48", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	return checkAddr(ip)
}

// local caches this machine's interface addresses. A public address on an
// interface (a VPS, a public IPv6 prefix) would otherwise reach services
// bound to all interfaces that a firewall hides from the outside.
var local struct {
	sync.Mutex
	at    time.Time
	addrs map[netip.Addr]bool
}

// interfaceAddrs lists the interface addresses; tests replace it.
var interfaceAddrs = net.InterfaceAddrs

func isLocal(ip netip.Addr) bool {
	local.Lock()
	defer local.Unlock()
	if time.Since(local.at) > 30*time.Second {
		if addrs, err := interfaceAddrs(); err == nil {
			local.addrs = map[netip.Addr]bool{}
			for _, a := range addrs {
				if p, err := netip.ParsePrefix(a.String()); err == nil {
					local.addrs[p.Addr().Unmap().WithZone("")] = true
				}
			}
			local.at = time.Now()
		}
	}
	return local.addrs[ip.Unmap().WithZone("")]
}

// isPublic reports whether ip is globally reachable, embedded IPv4 included.
func isPublic(ip netip.Addr) bool {
	if ip.Zone() != "" { // Prefix.Contains is false for zoned addresses; a public address has no zone
		return false
	}
	ip = ip.Unmap()
	if e2eLocal && ip.IsLoopback() {
		return true
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		return isPublic(netip.AddrFrom4([4]byte(b[12:16])))
	case sixToFour.Contains(ip):
		return isPublic(netip.AddrFrom4([4]byte(b[2:6])))
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
