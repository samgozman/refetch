// Package store is refetch's local event log: one SQLite file with the hook
// handler's tool calls and the MCP server's fetch attempts. Nothing in it
// leaves the machine.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the database/sql driver "sqlite"
)

// Tool call outcomes.
const (
	CallStarted    = "started"    // PreToolUse seen, no post event yet
	CallOK         = "ok"         // returned page text
	CallFailed     = "failed"     // tool error, or a page the block detector flags
	CallRedirected = "redirected" // denied by another hook in favor of another tool (context-mode)
	CallDenied     = "denied"     // denied for any other reason
)

// Retention is how long raw rows are kept before they are rolled into the
// daily table.
const Retention = 180 * 24 * time.Hour

// ToolCall is one web-reading tool call seen by the hook handler.
type ToolCall struct {
	Time      time.Time
	SessionID string
	ToolUseID string
	Tool      string
	URL       string // normalized on write
	Host      string // set on write
	Outcome   string
	Error     string // first 300 characters
	CCVersion string // Claude Code version
}

// Attempt is one step inside a fetch_page call (or refetch get).
type Attempt struct {
	Time     time.Time
	FetchID  string // groups the attempts of one call
	Via      string // mcp or cli
	URL      string // normalized on write
	Host     string // set on write
	Client   string
	Outcome  string
	Status   int
	Bytes    int
	Duration time.Duration
	Error    string // first 300 characters
}

// Store is an open events.db.
type Store struct {
	db   *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS tool_calls (
	id          INTEGER PRIMARY KEY,
	ts          INTEGER NOT NULL,
	session_id  TEXT NOT NULL DEFAULT '',
	tool_use_id TEXT NOT NULL DEFAULT '',
	tool        TEXT NOT NULL,
	url         TEXT NOT NULL DEFAULT '',
	host        TEXT NOT NULL DEFAULT '',
	outcome     TEXT NOT NULL,
	error       TEXT NOT NULL DEFAULT '',
	cc_version  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS tool_calls_ts ON tool_calls(ts);
CREATE UNIQUE INDEX IF NOT EXISTS tool_calls_use ON tool_calls(tool_use_id) WHERE tool_use_id != '';
CREATE TABLE IF NOT EXISTS attempts (
	id       INTEGER PRIMARY KEY,
	ts       INTEGER NOT NULL,
	fetch_id TEXT NOT NULL,
	via      TEXT NOT NULL,
	url      TEXT NOT NULL,
	host     TEXT NOT NULL,
	client   TEXT NOT NULL,
	outcome  TEXT NOT NULL,
	status   INTEGER NOT NULL DEFAULT 0,
	bytes    INTEGER NOT NULL DEFAULT 0,
	ms       INTEGER NOT NULL DEFAULT 0,
	error    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS attempts_ts ON attempts(ts);
CREATE TABLE IF NOT EXISTS daily (
	day     TEXT NOT NULL,
	kind    TEXT NOT NULL,
	name    TEXT NOT NULL,
	outcome TEXT NOT NULL,
	host    TEXT NOT NULL,
	n       INTEGER NOT NULL,
	PRIMARY KEY (day, kind, name, outcome, host)
);
PRAGMA user_version = 1;
`

// Path is events.db inside dir.
func Path(dir string) string { return filepath.Join(dir, "events.db") }

// Open opens or creates the database at path, applies the schema and rolls
// rows older than Retention into the daily table.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Rollup(ctx, time.Now().Add(-Retention)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// File is the database path.
func (s *Store) File() string { return s.path }

// RecordCall inserts a tool call, or updates the row with the same
// ToolUseID (a post event completing its PreToolUse record).
func (s *Store) RecordCall(ctx context.Context, c ToolCall) error {
	c.URL, c.Host = NormalizeURL(c.URL)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_calls (ts, session_id, tool_use_id, tool, url, host, outcome, error, cc_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tool_use_id) WHERE tool_use_id != '' DO UPDATE SET
			outcome = excluded.outcome, error = excluded.error,
			url = CASE WHEN excluded.url != '' THEN excluded.url ELSE url END,
			host = CASE WHEN excluded.host != '' THEN excluded.host ELSE host END`,
		ms(c.Time), c.SessionID, c.ToolUseID, c.Tool, c.URL, c.Host, c.Outcome, clip(c.Error), c.CCVersion)
	return err
}

// RecordAttempt inserts one fetch attempt.
func (s *Store) RecordAttempt(ctx context.Context, a Attempt) error {
	a.URL, a.Host = NormalizeURL(a.URL)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO attempts (ts, fetch_id, via, url, host, client, outcome, status, bytes, ms, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ms(a.Time), a.FetchID, a.Via, a.URL, a.Host, a.Client, a.Outcome, a.Status, a.Bytes, a.Duration.Milliseconds(), clip(a.Error))
	return err
}

// Calls returns the tool calls since t, oldest first.
func (s *Store) Calls(ctx context.Context, since time.Time) ([]ToolCall, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, session_id, tool_use_id, tool, url, host, outcome, error, cc_version
		FROM tool_calls WHERE ts >= ? ORDER BY ts, id`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ToolCall
	for rows.Next() {
		var c ToolCall
		var ts int64
		if err := rows.Scan(&ts, &c.SessionID, &c.ToolUseID, &c.Tool, &c.URL, &c.Host, &c.Outcome, &c.Error, &c.CCVersion); err != nil {
			return nil, err
		}
		c.Time = time.UnixMilli(ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Attempts returns the attempts since t, oldest first.
func (s *Store) Attempts(ctx context.Context, since time.Time) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, fetch_id, via, url, host, client, outcome, status, bytes, ms, error
		FROM attempts WHERE ts >= ? ORDER BY ts, id`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		var ts, dur int64
		if err := rows.Scan(&ts, &a.FetchID, &a.Via, &a.URL, &a.Host, &a.Client, &a.Outcome, &a.Status, &a.Bytes, &dur, &a.Error); err != nil {
			return nil, err
		}
		a.Time, a.Duration = time.UnixMilli(ts), time.Duration(dur)*time.Millisecond
		out = append(out, a)
	}
	return out, rows.Err()
}

// Rollup moves rows older than cutoff into per-day counts.
func (s *Store) Rollup(ctx context.Context, cutoff time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := ms(cutoff)
	for _, q := range []string{
		`INSERT INTO daily (day, kind, name, outcome, host, n)
		 SELECT date(ts / 1000, 'unixepoch'), 'tool_call', tool, outcome, host, count(*) FROM tool_calls WHERE ts < ?
		 GROUP BY 1, 3, 4, 5 ON CONFLICT DO UPDATE SET n = n + excluded.n`,
		`DELETE FROM tool_calls WHERE ts < ?`,
		`INSERT INTO daily (day, kind, name, outcome, host, n)
		 SELECT date(ts / 1000, 'unixepoch'), 'attempt', client, outcome, host, count(*) FROM attempts WHERE ts < ?
		 GROUP BY 1, 3, 4, 5 ON CONFLICT DO UPDATE SET n = n + excluded.n`,
		`DELETE FROM attempts WHERE ts < ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, c); err != nil {
			return fmt.Errorf("roll-up: %w", err)
		}
	}
	return tx.Commit()
}

// Reset deletes every row.
func (s *Store) Reset(ctx context.Context) error {
	for _, q := range []string{"DELETE FROM tool_calls", "DELETE FROM attempts", "DELETE FROM daily"} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, "VACUUM")
	return err
}

// Size is the database file size in bytes, WAL included.
func (s *Store) Size() int64 {
	var n int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// NormalizeURL lowercases the scheme and host, drops user info, default
// ports, the query string and the fragment, and returns the URL and its
// host. A string that is not an absolute URL comes back as is.
func NormalizeURL(raw string) (normalized, host string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw, ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	u.Host = host
	if port != "" {
		u.Host += ":" + port
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), host
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UnixMilli()
}

func clip(s string) string {
	if r := []rune(s); len(r) > 300 {
		return string(r[:300])
	}
	return s
}
