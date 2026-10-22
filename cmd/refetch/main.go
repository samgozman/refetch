// Command refetch reads web pages that Claude Code's WebFetch could not: it
// is the fetch_page MCP server, the manual fetcher and the stats report.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"
	"uuid"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
	"github.com/samgozman/refetch/store"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `refetch reads web pages that WebFetch could not.

Usage:
  refetch mcp                 run the fetch_page MCP server on stdio
  refetch get [flags] <url>   fetch one page and show which step won
  refetch stats [flags]       how often refetch saved the day
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

// fetcher is the shared fetch.Fetcher plus where its attempts are recorded.
type fetcher struct {
	f   *fetch.Fetcher
	st  *store.Store // nil when stats are off or the store could not open
	via string
}

func newFetcher(ctx context.Context, cfg config.Config, via string, stderr io.Writer) *fetcher {
	ua := cfg.UserAgent
	if ua == "" {
		ua = "refetch/" + buildVersion()
	}
	fr := &fetcher{via: via, f: &fetch.Fetcher{
		Impersonate:   cfg.Impersonate,
		RespectRobots: cfg.RespectRobots,
		UserAgent:     ua,
		Timeout:       cfg.Timeout.Duration,
	}}
	if cfg.Stats {
		st, err := store.Open(ctx, store.Path(config.DataDir()))
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "refetch: stats off for this run:", err)
		}
		fr.st = st
	}
	return fr
}

// get fetches one page and records its attempts under one fetch id.
func (fr *fetcher) get(ctx context.Context, url string) (fetch.Page, error) {
	p, err := fr.f.Get(ctx, url)
	if fr.st != nil {
		id := uuid.New().String()
		for _, a := range p.Attempts {
			_ = fr.st.RecordAttempt(context.WithoutCancel(ctx), store.Attempt{
				Time: time.Now(), FetchID: id, Via: fr.via, URL: a.URL, Client: a.Client, Outcome: a.Outcome,
				Status: a.Status, Bytes: a.Bytes, Duration: a.Duration, Error: a.Err,
			})
		}
	}
	return p, err
}

func (fr *fetcher) close() {
	if fr.st != nil {
		_ = fr.st.Close()
	}
}
