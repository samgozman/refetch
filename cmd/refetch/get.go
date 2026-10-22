package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/fetch"
)

func runGet(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxChars := fs.Int("max-chars", defaultMaxChars, "text limit in characters (0 = no limit)")
	impersonate := fs.String("impersonate", cfg.Impersonate, "always, fallback or off")
	raw := fs.Bool("raw", false, "print the response body instead of its text")
	allowRender := fs.Bool("render", true, "run the headless Chrome step for a challenge or JavaScript-only page")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: refetch get [flags] <url>\n\nFetches one page, prints each step to stderr and the text to stdout.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("get takes exactly one URL")
	}
	cfg.Impersonate = *impersonate
	if err := cfg.Validate(); err != nil {
		return err
	}
	fr := newFetcher(ctx, cfg, "cli", stderr)
	defer fr.close()
	p, err := fr.get(ctx, fs.Arg(0), *allowRender)
	for _, a := range p.Attempts {
		_, _ = fmt.Fprintf(stderr, "%-13s %-15s HTTP %-3d %8d B %6.2fs  %s\n", a.Client, a.Outcome, a.Status, a.Bytes, a.Duration.Seconds(), a.Err)
	}
	if err != nil {
		if note := skipNote(p); note != "" {
			_, _ = fmt.Fprintln(stderr, note)
		}
		return err
	}
	_, _ = fmt.Fprintf(stderr, "won: %s\n\n", clientNames[p.Last.Client])
	out := string(p.Body)
	if !*raw {
		out = fetch.Text(p.Body, p.ContentType)
	}
	if *maxChars > 0 {
		out = fetch.Clip(out, *maxChars)
	}
	_, err = fmt.Fprintln(stdout, out)
	return err
}
