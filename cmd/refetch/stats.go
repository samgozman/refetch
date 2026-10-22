package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/stats"
	"github.com/samgozman/refetch/store"
)

func runStats(ctx context.Context, _ config.Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "30d", "period to report, like 7d, 12h or 90d")
	by := fs.String("by", "", "\"host\" for a per-host table")
	session := fs.String("session", "", "only this Claude Code session id")
	asJSON := fs.Bool("json", false, "print JSON")
	reset := fs.Bool("reset", false, "delete all recorded data")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: refetch stats [flags]\n\nHow many web reads failed, how many refetch picked up and how many it resolved.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *by != "" && *by != "host" {
		return fmt.Errorf("--by %q: only \"host\" is supported", *by)
	}
	period, err := parseSince(*since)
	if err != nil {
		return err
	}
	path := store.Path(config.DataDir())
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && !*reset {
		_, _ = fmt.Fprintf(stdout, "refetch %s · no data yet (%s does not exist)\n", buildVersion(), path)
		return nil
	}
	st, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if *reset {
		if err := st.Reset(ctx); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, "refetch: all recorded data deleted from", path)
		return nil
	}
	now := time.Now()
	from := now.Add(-period)
	calls, err := st.Calls(ctx, from)
	if err != nil {
		return err
	}
	attempts, err := st.Attempts(ctx, from)
	if err != nil {
		return err
	}
	r := stats.Build(calls, attempts, stats.Options{Since: from, Session: *session, ByHost: *by == "host"}, now)
	if *asJSON {
		return json.MarshalWrite(stdout, r, jsontext.WithIndentPrefix(""), jsontext.WithIndent("  "))
	}
	r.WriteText(stdout, stats.Header{Version: buildVersion(), DataPath: path, Bytes: st.Size()})
	return nil
}

// parseSince accepts a number of days ("7d") or a Go duration ("12h").
func parseSince(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--since %q: want a number of days like 7d", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--since %q: want 7d, 12h or similar", s)
	}
	return d, nil
}
