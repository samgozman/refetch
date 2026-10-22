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
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"
	"uuid"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
	"github.com/samgozman/refetch/render"
	"github.com/samgozman/refetch/store"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

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
	f         *fetch.Fetcher
	r         renderer     // the headless Chrome step
	renderOff bool         // render = "off"
	st        *store.Store // nil when stats are off or the store could not open
	via       string
	stderr    io.Writer // where recording errors go (the MCP server's log)
}

type renderer interface {
	Render(ctx context.Context, url string) ([]byte, error)
}

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
		f:         newFetchClient(cfg),
		r:         &render.Renderer{Timeout: cfg.RenderTimeout.Duration},
		renderOff: cfg.Render == "off",
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

// get fetches one page: the HTTP clients first, then the headless Chrome
// step when they got a challenge or a JavaScript-only page. allowRender
// false (fetch_page's render: false) skips that step for this call. All
// attempts are recorded under one fetch id.
func (fr *fetcher) get(ctx context.Context, url string, allowRender bool) (fetch.Page, error) {
	p, err := fr.f.Get(ctx, url)
	if err != nil && (p.Last.Outcome == fetch.OutcomeChallenge || p.Last.Outcome == fetch.OutcomeNeedsJS) {
		p, err = fr.renderStep(ctx, url, allowRender, p, err)
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

// renderStep runs step 2, or records why it did not run. A skipped step
// leaves p.Last on the HTTP attempt, whose outcome the hand-off explains.
func (fr *fetcher) renderStep(ctx context.Context, url string, allowRender bool, p fetch.Page, httpErr error) (fetch.Page, error) {
	a := fetch.Attempt{URL: url, Host: p.Last.Host, Client: render.Client}
	var dom []byte
	switch {
	case fr.renderOff:
		a.Outcome = render.OutcomeSkippedOff
	case !allowRender:
		a.Outcome = render.OutcomeSkippedByCall
	default:
		start := time.Now()
		var err error
		dom, err = fr.r.Render(ctx, url)
		a.Duration, a.Bytes = time.Since(start), len(dom)
		switch {
		case errors.Is(err, render.ErrNoChrome):
			a.Outcome = render.OutcomeNoChrome
		case errors.Is(err, render.ErrRoot):
			a.Outcome = render.OutcomeSkippedRoot
		case errors.Is(err, render.ErrTimeout):
			a.Outcome, a.Err = fetch.OutcomeTimeout, err.Error()
		case err != nil:
			a.Outcome, a.Err = fetch.OutcomeError, err.Error()
		case fetch.Challenge(http.StatusOK, dom):
			a.Outcome, a.Err = fetch.OutcomeChallenge, "headless Chrome got the challenge page too"
		case fetch.EmptyRendered(dom):
			a.Outcome, a.Err = fetch.OutcomeNeedsJS, "the page was still empty after headless Chrome ran its scripts"
		default:
			a.Outcome = fetch.OutcomeOK
		}
	}
	p.Attempts = append(p.Attempts, a)
	switch a.Outcome {
	case fetch.OutcomeOK:
		p.Body, p.ContentType, p.Last = dom, "text/html", a
		return p, nil
	case render.OutcomeSkippedOff, render.OutcomeSkippedByCall, render.OutcomeNoChrome, render.OutcomeSkippedRoot:
		return p, httpErr
	}
	p.Last = a
	return p, fmt.Errorf("%w; then %s", httpErr, cmp.Or(a.Err, a.Outcome))
}

func (fr *fetcher) close() {
	if fr.st != nil {
		_ = fr.st.Close()
	}
}
