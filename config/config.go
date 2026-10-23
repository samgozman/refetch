// Package config loads refetch's settings (defaults, config.toml, REFETCH_*
// variables), so the hooks, the MCP server and the CLI read the same switches.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config holds every switch. The zero value is not valid; use Default or
// Load.
type Config struct {
	Impersonate   string   `toml:"impersonate"`    // always, fallback or off
	Render        string   `toml:"render"`         // auto or off (headless Chrome step)
	RenderTimeout Duration `toml:"render_timeout"` // hard cap for the headless Chrome step
	Timeout       Duration `toml:"timeout"`        // per HTTP client
	RespectRobots bool     `toml:"respect_robots"` // consult robots.txt
	Stats         bool     `toml:"stats"`          // record events.db rows
	Mode          string   `toml:"mode"`           // nudge or inline (hook response)
	UserAgent     string   `toml:"user_agent"`     // the plain client's User-Agent; empty = refetch/<version>
	UseProxy      bool     `toml:"use_proxy"`      // HTTPS_PROXY outside cloud sessions; a TLS-decrypting one replaces the Chrome handshake
}

// Duration is a time.Duration written as "10s" in TOML and env vars.
type Duration struct{ time.Duration }

// UnmarshalText parses a Go duration string.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText writes the duration as a Go duration string.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Default is the configuration with nothing set: everything on, robots off.
func Default() Config {
	return Config{
		Impersonate:   "always",
		Render:        "auto",
		RenderTimeout: Duration{15 * time.Second},
		Timeout:       Duration{20 * time.Second},
		Stats:         true,
		Mode:          "nudge",
	}
}

// Path is the config file: REFETCH_CONFIG, else
// $XDG_CONFIG_HOME/refetch/config.toml, else ~/.config/refetch/config.toml.
func Path() string {
	if p := os.Getenv("REFETCH_CONFIG"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "refetch", "config.toml")
}

// DataDir holds events.db and saved pages: REFETCH_DATA, else
// $XDG_DATA_HOME/refetch, else ~/.local/share/refetch.
func DataDir() string {
	if p := os.Getenv("REFETCH_DATA"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "refetch")
}

// Load reads the config file if it exists, applies REFETCH_* overrides and
// validates the result.
func Load() (Config, error) {
	c := Default()
	path := Path()
	if md, err := toml.DecodeFile(path, &c); err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, fmt.Errorf("%s: %w", path, err)
	} else if err == nil {
		if keys := md.Undecoded(); len(keys) > 0 {
			return c, fmt.Errorf("%s: unknown setting %q", path, keys[0].String())
		}
	}
	if err := c.applyEnv(os.Getenv); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c *Config) applyEnv(getenv func(string) string) error {
	strs := map[string]*string{
		"REFETCH_IMPERSONATE": &c.Impersonate,
		"REFETCH_RENDER":      &c.Render,
		"REFETCH_MODE":        &c.Mode,
		"REFETCH_USER_AGENT":  &c.UserAgent,
	}
	for k, p := range strs {
		if v := getenv(k); v != "" {
			*p = v
		}
	}
	durs := map[string]*Duration{"REFETCH_RENDER_TIMEOUT": &c.RenderTimeout, "REFETCH_TIMEOUT": &c.Timeout}
	for k, p := range durs {
		if v := getenv(k); v != "" {
			if err := p.UnmarshalText([]byte(v)); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	bools := map[string]*bool{"REFETCH_RESPECT_ROBOTS": &c.RespectRobots, "REFETCH_STATS": &c.Stats, "REFETCH_USE_PROXY": &c.UseProxy}
	for k, p := range bools {
		if v := getenv(k); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			*p = b
		}
	}
	return nil
}

// Cloud reports a Claude Code cloud session, online only through its proxy.
func Cloud() bool { return os.Getenv("CLAUDE_CODE_REMOTE") == "true" }

// ProxyOn reports whether connections go through HTTPS_PROXY.
func (c Config) ProxyOn() bool { return c.UseProxy || Cloud() }

// Validate checks the enumerated settings and durations.
func (c Config) Validate() error {
	var errs []error
	check := func(name, v string, allowed ...string) {
		if slices.Contains(allowed, v) {
			return
		}
		errs = append(errs, fmt.Errorf("%s = %q, want one of %s", name, v, strings.Join(allowed, ", ")))
	}
	check("impersonate", c.Impersonate, "always", "fallback", "off")
	check("render", c.Render, "auto", "off")
	check("mode", c.Mode, "nudge", "inline")
	if c.Timeout.Duration <= 0 || c.RenderTimeout.Duration <= 0 {
		errs = append(errs, errors.New("timeout and render_timeout must be positive"))
	}
	return errors.Join(errs...)
}
