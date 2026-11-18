// Command refetch reads web pages that Claude Code's WebFetch could not: it
// is the fetch_page MCP server, the manual fetcher and the stats report.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"uuid"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
	"github.com/samgozman/refetch/render"
	"github.com/samgozman/refetch/site"
	"github.com/samgozman/refetch/store"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

// readPDFCmd is the hidden subcommand that reads one PDF from stdin for
// fetch.IsolatePDF.
const readPDFCmd = "read-pdf"

const usage = `refetch reads web pages that WebFetch could not.

Usage:
  refetch mcp                 run the fetch_page MCP server on stdio
  refetch get [flags] <url>   fetch one page and show which step won
  refetch stats [flags]       how often refetch saved the day
  refetch hook <event>        Claude Code hook handler (reads the event JSON on stdin)
  refetch doctor              check the setup: version, permission, Chrome, other hooks
  refetch version             print the version

Settings: %s (and REFETCH_* environment variables)
Data:     %s
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if exe, err := os.Executable(); err == nil { // a PDF that crashes the parser ends a child, not the server
		fetch.IsolatePDF(exe, readPDFCmd)
	}
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		_, _ = fmt.Fprintf(stderr, usage, config.Path(), config.DataDir())
		return 2
	}
	cmd, args := args[0], args[1:]
	if cmd == "version" || cmd == "--version" {
		_, _ = fmt.Fprintln(stdout, "refetch", buildVersion())
		return 0
	}
	switch cmd {
	case "hook":
		runHook(ctx, args, os.Stdin, stdout, stderr)
		return 0
	case readPDFCmd:
		return fetch.ServePDF(ctx, os.Stdin, stdout, stderr)
	case "doctor":
		if err := runDoctor(ctx, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, "refetch:", err)
			return 1
		}
		return 0
	}
	cfg, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "refetch:", err)
		return 1
	}
	useProxy(cfg)
	switch cmd {
	case "mcp":
		err = runMCP(ctx, cfg, stderr)
	case "get":
		err = runGet(ctx, cfg, args, stdout, stderr)
	case "stats":
		err = runStats(ctx, cfg, args, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "refetch: unknown command %q\n\n", cmd)
		_, _ = fmt.Fprintf(stderr, usage, config.Path(), config.DataDir())
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "refetch:", err)
		return 1
	}
	return 0
}

// useProxy routes connections through HTTPS_PROXY when use_proxy is on,
// and returns the proxy's address ("" for none).
func useProxy(cfg config.Config) string {
	if !cfg.UseProxy {
		return ""
	}
	return fetch.UseProxy()
}

// buildVersion is the release version, or the module version for go
// install builds.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

// fetcher is the shared fetch.Fetcher, the headless Chrome step and where
// their attempts are recorded.
type fetcher struct {
	f         pageGetter   // the HTTP clients
	r         renderer     // the headless Chrome step
	renderOff bool         // render = "off"
	archive   bool         // archive = "wayback"; off in the zero value
	st        *store.Store // nil when stats are off or the store could not open
	via       string
	stderr    io.Writer // where recording errors go (the MCP server's log)

	mu       sync.Mutex
	cooldown map[string]time.Time // API host → when refetch asks it again
	now      func() time.Time     // the clock; time.Now when nil
}

type renderer interface {
	Render(ctx context.Context, url string) (render.Page, error)
}

type pageGetter interface {
	Get(ctx context.Context, url string) (fetch.Page, error)
}

// pagesFor builds the HTTP clients for the settings; tests replace it.
var pagesFor = func(cfg config.Config) pageGetter { return newFetchClient(cfg) }

func newFetcher(ctx context.Context, cfg config.Config, via string, stderr io.Writer) *fetcher {
	var st *store.Store
	if cfg.Stats {
		var err error
		if st, err = store.Open(ctx, store.Path(config.DataDir())); err != nil {
			_, _ = fmt.Fprintln(stderr, "refetch: stats off for this run:", err)
		}
	}
	return newFetcherWith(cfg, st, via, stderr)
}

// newFetcherWith builds the fetcher on an already open store (nil = none).
func newFetcherWith(cfg config.Config, st *store.Store, via string, stderr io.Writer) *fetcher {
	return &fetcher{
		f:         pagesFor(cfg),
		r:         &render.Renderer{Timeout: cfg.RenderTimeout.Duration},
		renderOff: cfg.Render == "off",
		archive:   cfg.Archive == "wayback",
		st:        st, via: via, stderr: stderr,
	}
}

// newFetchClient is the fetch.Fetcher the settings describe.
func newFetchClient(cfg config.Config) *fetch.Fetcher {
	return &fetch.Fetcher{
		Impersonate:   cfg.Impersonate,
		RespectRobots: cfg.RespectRobots,
		UserAgent:     cmp.Or(cfg.UserAgent, "refetch/"+buildVersion()),
		Timeout:       cfg.Timeout.Duration,
	}
}

// renderMode is fetch_page's render argument for one call.
type renderMode int

const (
	renderAuto   renderMode = iota // headless Chrome for a challenge or a JavaScript-only page
	renderNever                    // render: false
	renderAlways                   // render: true, also for a page the HTTP clients read or were refused
)

// get fetches one page: the HTTP clients first, then the headless Chrome
// step when they got a challenge or a JavaScript-only page, or always with
// renderAlways. A host that recently needed headless Chrome every time
// goes to it first; a page none of them read, to the Wayback Machine. All
// attempts are recorded under one fetch id.
func (fr *fetcher) get(ctx context.Context, url string, mode renderMode) (fetch.Page, error) {
	if !fetch.IsWebURL(url) {
		return fetch.Page{}, fmt.Errorf("not an http(s) URL: %q", url)
	}
	url = cmp.Or(fetch.Rewrite(url), url)
	p, err := fr.read(ctx, url, mode)
	if fr.archiveWorthy(ctx, url, p, err) {
		p, err = fr.viaArchive(ctx, url, p, err)
	}
	if fr.st != nil {
		id := uuid.New().String()
		for _, a := range p.Attempts {
			err := fr.st.RecordAttempt(context.WithoutCancel(ctx), store.Attempt{
				Time: time.Now(), FetchID: id, Via: fr.via, URL: a.URL, Client: a.Client, Outcome: a.Outcome,
				Status: a.Status, Bytes: a.Bytes, Duration: a.Duration, Error: a.Err,
			})
			if err != nil && fr.stderr != nil {
				_, _ = fmt.Fprintln(fr.stderr, "refetch: could not record a fetch step:", err)
			}
		}
	}
	return p, err
}

// read runs the steps for one page: a site's API for a page its address
// names (a Stack Exchange question), else the HTTP clients and headless
// Chrome, then a site's API for a page its markup names (a Discourse
// topic). A JavaScript-only page's markup is tried before headless Chrome
// (a Mastodon instance). render: true goes to the page itself.
func (fr *fetcher) read(ctx context.Context, url string, mode renderMode) (fetch.Page, error) {
	var tried []fetch.Attempt
	if r := site.ForURL(url); r != nil && mode != renderAlways {
		sp, ok := fr.viaSite(ctx, url, r, fetch.Page{})
		if ok {
			return sp, nil
		}
		tried = sp.Attempts
	}
	var p fetch.Page
	var err error
	pageRoute := mode != renderAlways
	if mode == renderAuto && fr.remembered(ctx, url) {
		p, err = fr.renderFirst(ctx, url)
	} else {
		p, err = fr.f.Get(ctx, url)
		if r := shellRoute(url, p, pageRoute); r != nil {
			sp, ok := fr.viaSite(ctx, url, r, p)
			if ok {
				sp.Attempts = append(tried, sp.Attempts...)
				return sp, nil
			}
			// Not again on headless Chrome's render of the same page.
			p.Attempts, pageRoute = sp.Attempts, false
		}
		switch o := p.Last.Outcome; {
		case err != nil && (o == fetch.OutcomeChallenge || o == fetch.OutcomeNeedsJS),
			mode == renderAlways && (err == nil && p.ContentType != fetch.TypeMarkdown || o == fetch.OutcomeRefused):
			p, err = fr.renderStep(ctx, url, mode, p, err)
		}
	}
	p.Attempts = append(tried, p.Attempts...)
	if err != nil && p.Shell != nil {
		if sp, ok := structuredPage(url, p); ok {
			return sp, nil
		}
	}
	if err == nil && pageRoute && strings.Contains(p.ContentType, "html") {
		if r := site.ForPage(cmp.Or(p.URL, url), p.Body); r != nil {
			sp, _ := fr.viaSite(ctx, url, r, p)
			return sp, nil
		}
	}
	return p, err
}

// shellRoute is the site route a JavaScript-only page's markup names.
func shellRoute(url string, p fetch.Page, allowed bool) *site.Route {
	if !allowed || p.Last.Outcome != fetch.OutcomeNeedsJS || p.Shell == nil {
		return nil
	}
	return site.ForPage(cmp.Or(p.URL, url), p.Shell)
}

// structuredPage reads a JavaScript-only page that headless Chrome did not
// fill from the structured data in its HTML, when that holds the text.
func structuredPage(rawURL string, p fetch.Page) (fetch.Page, bool) {
	base, _ := neturl.Parse(cmp.Or(p.URL, rawURL))
	md := fetch.Structured(p.Shell, base)
	if md == "" {
		return p, false
	}
	_, host := store.NormalizeURL(rawURL)
	a := fetch.Attempt{URL: rawURL, Host: host, Client: fetch.ClientStructured, Outcome: fetch.OutcomeOK, Bytes: len(md)}
	p.Body, p.ContentType, p.Last, p.Via = []byte(md), fetch.TypeMarkdown, a, "page's structured data"
	p.Attempts = append(p.Attempts, a)
	return p, true
}

// apiCooldown is how long an API host that refused refetch for its rate
// limit is left alone: its route is skipped and the page read as usual.
const apiCooldown = time.Hour

// errCooling stops a route at an API host in its cooldown, before a request.
var errCooling = errors.New("API host cooling down after a rate limit")

// viaSite reads url through a site's API, as one attempt after p's. On
// success the page is the API's Markdown; on failure p comes back with the
// failed attempt added. A route whose API is cooling down adds nothing.
func (fr *fetcher) viaSite(ctx context.Context, url string, r *site.Route, p fetch.Page) (fetch.Page, bool) {
	_, host := store.NormalizeURL(url)
	a := fetch.Attempt{URL: url, Host: host, Client: site.Client}
	var apiHost string
	var asked, skipped, got429 bool
	start := time.Now()
	md, err := r.Read(ctx, func(ctx context.Context, u string) ([]byte, error) {
		_, apiHost = store.NormalizeURL(u)
		if fr.cooling(apiHost) {
			skipped = true
			return nil, errCooling
		}
		asked = true
		got, err := fr.f.Get(ctx, u)
		a.Status = got.Last.Status
		if err != nil {
			got429 = got429 || got.Last.Status == http.StatusTooManyRequests
			return got.ErrorBody, err
		}
		return got.Body, nil
	})
	if skipped && !asked {
		return p, false
	}
	if err != nil && (got429 || errors.Is(err, site.ErrRateLimited)) {
		fr.coolDown(apiHost)
	}
	a.Duration, a.Bytes = time.Since(start), len(md)
	if err != nil {
		a.Outcome, a.Err = fetch.OutcomeError, r.Name+": "+err.Error()
		if isTimeoutErr(err) {
			a.Outcome = fetch.OutcomeTimeout
		}
		p.Attempts = append(p.Attempts, a)
		return p, false
	}
	a.Outcome = fetch.OutcomeOK
	return fetch.Page{Body: []byte(md), ContentType: fetch.TypeMarkdown, URL: url, Last: a, Attempts: append(p.Attempts, a), Via: r.Name}, true
}

// cooling reports whether apiHost is in its cooldown.
func (fr *fetcher) cooling(apiHost string) bool {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.clock().Before(fr.cooldown[apiHost])
}

// coolDown starts apiHost's cooldown. It lives in this process only: the
// MCP server runs for the whole session, while refetch get reads one page.
func (fr *fetcher) coolDown(apiHost string) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.cooldown == nil {
		fr.cooldown = map[string]time.Time{}
	}
	fr.cooldown[apiHost] = fr.clock().Add(apiCooldown)
}

func (fr *fetcher) clock() time.Time {
	if fr.now != nil {
		return fr.now()
	}
	return time.Now()
}

func isTimeoutErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout")
}

// Remembered hosts: headless Chrome alone read the host's last
// rememberFetches fetches, within rememberFor. Past that the HTTP clients
// get another try, in case the site changed.
const (
	rememberFetches = 2
	rememberFor     = 7 * 24 * time.Hour
)

// remembered reports whether url's host goes to headless Chrome first.
func (fr *fetcher) remembered(ctx context.Context, url string) bool {
	if fr.st == nil || fr.renderOff {
		return false
	}
	_, host := store.NormalizeURL(url)
	ok, err := fr.st.NeedsRender(ctx, host, time.Now().Add(-rememberFor), rememberFetches)
	return err == nil && ok
}

// renderFirst reads a remembered host's page with headless Chrome, and with
// the HTTP clients only if that fails; headless Chrome does not run twice.
func (fr *fetcher) renderFirst(ctx context.Context, url string) (fetch.Page, error) {
	_, host := store.NormalizeURL(url)
	a, rp := fr.renderAttempt(ctx, url, host)
	if a.Outcome == fetch.OutcomeOK {
		return renderedPage(fetch.Page{Attempts: []fetch.Attempt{a}}, a, rp), nil
	}
	p, err := fr.f.Get(ctx, url)
	p.Attempts = append([]fetch.Attempt{a}, p.Attempts...)
	return p, err
}

// renderStep runs step 2, or records why it did not run. A skipped step
// leaves p.Last on the HTTP attempt, whose outcome the hand-off explains;
// so does a failed one after the HTTP clients read the page.
func (fr *fetcher) renderStep(ctx context.Context, url string, mode renderMode, p fetch.Page, httpErr error) (fetch.Page, error) {
	a := fetch.Attempt{URL: url, Host: p.Last.Host, Client: render.Client}
	var rp render.Page
	switch {
	case fr.renderOff:
		a.Outcome = render.OutcomeSkippedOff
	case mode == renderNever:
		a.Outcome = render.OutcomeSkippedByCall
	default:
		a, rp = fr.renderAttempt(ctx, url, p.Last.Host)
	}
	p.Attempts = append(p.Attempts, a)
	switch a.Outcome {
	case fetch.OutcomeOK:
		return renderedPage(p, a, rp), nil
	case render.OutcomeSkippedOff, render.OutcomeSkippedByCall, render.OutcomeNoChrome, render.OutcomeSkippedRoot:
		return p, httpErr
	}
	if httpErr == nil {
		return p, nil
	}
	p.Last = a
	return p, fmt.Errorf("%w; then %s", httpErr, cmp.Or(a.Err, a.Outcome))
}

// renderAttempt runs headless Chrome on url and names the outcome.
func (fr *fetcher) renderAttempt(ctx context.Context, url, host string) (fetch.Attempt, render.Page) {
	a := fetch.Attempt{URL: url, Host: host, Client: render.Client}
	start := time.Now()
	rp, err := fr.r.Render(ctx, url)
	a.Duration, a.Bytes = time.Since(start), len(rp.DOM)
	switch {
	case errors.Is(err, render.ErrNoChrome):
		a.Outcome = render.OutcomeNoChrome
	case errors.Is(err, render.ErrRoot):
		a.Outcome = render.OutcomeSkippedRoot
	case errors.Is(err, render.ErrTimeout):
		a.Outcome, a.Err = fetch.OutcomeTimeout, err.Error()
	case err != nil:
		a.Outcome, a.Err = fetch.OutcomeError, err.Error()
	case fetch.Challenge(http.StatusOK, rp.DOM):
		a.Outcome, a.Err = fetch.OutcomeChallenge, "headless Chrome got the challenge page too"
	case fetch.EmptyRendered(rp.DOM):
		a.Outcome, a.Err = fetch.OutcomeNeedsJS, "the page was still empty after headless Chrome ran its scripts"
	default:
		a.Outcome = fetch.OutcomeOK
	}
	return a, rp
}

// renderedPage is p with headless Chrome's DOM as its body.
func renderedPage(p fetch.Page, a fetch.Attempt, rp render.Page) fetch.Page {
	p.Body, p.ContentType, p.Last = rp.DOM, "text/html", a
	if strings.HasPrefix(rp.URL, "https://") || strings.HasPrefix(rp.URL, "http://") { // where links resolve
		p.URL = rp.URL
	}
	return p
}

func (fr *fetcher) close() {
	if fr.st != nil {
		_ = fr.st.Close()
	}
}
