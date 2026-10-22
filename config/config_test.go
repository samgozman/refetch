package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsWithoutFile(t *testing.T) {
	t.Setenv("REFETCH_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c != Default() {
		t.Errorf("got %+v", c)
	}
	if c.Impersonate != "always" || c.Render != "auto" || !c.Stats || c.RespectRobots || c.Mode != "nudge" {
		t.Errorf("defaults changed: %+v", c)
	}
}

func TestFileThenEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("impersonate = \"off\"\nrender_timeout = \"15s\"\nstats = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REFETCH_CONFIG", path)
	t.Setenv("REFETCH_STATS", "true")
	t.Setenv("REFETCH_RESPECT_ROBOTS", "1")
	t.Setenv("REFETCH_TIMEOUT", "5s")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Impersonate != "off" || c.RenderTimeout.Duration != 15*time.Second || !c.Stats || !c.RespectRobots || c.Timeout.Duration != 5*time.Second {
		t.Errorf("got %+v", c)
	}
}

func TestInvalid(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"bad mode":    "mode = \"loud\"\n",
		"unknown key": "impersonat = \"off\"\n",
		"bad dur":     "timeout = \"soon\"\n",
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("REFETCH_CONFIG", path)
		if _, err := Load(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestDirs(t *testing.T) {
	t.Setenv("REFETCH_DATA", "")
	t.Setenv("XDG_DATA_HOME", "/x/data")
	if got := DataDir(); got != "/x/data/refetch" {
		t.Errorf("DataDir = %s", got)
	}
	t.Setenv("REFETCH_DATA", "/y")
	if got := DataDir(); got != "/y" {
		t.Errorf("DataDir = %s", got)
	}
	t.Setenv("REFETCH_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	if got := Path(); got != "/x/config/refetch/config.toml" {
		t.Errorf("Path = %s", got)
	}
}
