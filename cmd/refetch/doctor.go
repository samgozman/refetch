package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/samgozman/refetch/config"
	"github.com/samgozman/refetch/render"
	"github.com/samgozman/refetch/store"
)

type check struct {
	ok     bool
	name   string
	detail string
}

// runDoctor checks the setup and prints one line per check. It reports
// problems in its output and returns an error only if it cannot write.
func runDoctor(ctx context.Context, stdout io.Writer) error {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	env := claudeEnv{home: home, cwd: cwd}
	checks := []check{versionCheck()}
	cfg, err := config.Load()
	if err != nil {
		checks = append(checks, check{false, "config", err.Error()})
		cfg = config.Default()
	} else {
		checks = append(checks, configCheck())
	}
	checks = append(checks, dataCheck(ctx, cfg), chromeCheck(cfg), env.permissionCheck(), env.hooksCheck())
	for _, c := range checks {
		mark := "✓"
		if !c.ok {
			mark = "!"
		}
		if _, err := fmt.Fprintf(stdout, "%s %-12s %s\n", mark, c.name, strings.ReplaceAll(c.detail, "\n", "\n               ")); err != nil {
			return err
		}
	}
	return nil
}

func versionCheck() check {
	bin := buildVersion()
	plugin := os.Getenv("REFETCH_PLUGIN_VERSION")
	switch {
	case plugin == "":
		return check{true, "version", "refetch " + bin + " (not run through the plugin)"}
	case strings.TrimPrefix(bin, "v") != plugin:
		return check{false, "version", fmt.Sprintf("binary %s, plugin %s: run /reload-plugins, or check REFETCH_BIN", bin, plugin)}
	}
	return check{true, "version", "refetch " + bin + ", same as the plugin"}
}

func configCheck() check {
	path := config.Path()
	if _, err := os.Stat(path); err != nil {
		return check{true, "config", "defaults (no " + path + ")"}
	}
	return check{true, "config", path}
}

func dataCheck(ctx context.Context, cfg config.Config) check {
	if !cfg.Stats {
		return check{true, "stats", "off (stats = false); nothing is recorded"}
	}
	path := store.Path(config.DataDir())
	st, err := store.Open(ctx, path)
	if err != nil {
		return check{false, "stats", "cannot open " + path + ": " + err.Error()}
	}
	defer func() { _ = st.Close() }()
	since := time.Now().Add(-30 * 24 * time.Hour)
	calls, err1 := st.Calls(ctx, since)
	attempts, err2 := st.Attempts(ctx, since)
	if err1 != nil || err2 != nil {
		return check{false, "stats", fmt.Sprintf("cannot read %s: %v %v", path, err1, err2)}
	}
	return check{true, "stats", fmt.Sprintf("%s: %d tool calls and %d fetch steps in the last 30 days", path, len(calls), len(attempts))}
}

func chromeCheck(cfg config.Config) check {
	if cfg.Render == "off" {
		return check{true, "chrome", "headless Chrome step off (render = \"off\")"}
	}
	if p := render.FindChrome(); p != "" {
		return check{true, "chrome", "found " + p}
	}
	return check{false, "chrome", "no Chrome or Chromium found, so the headless Chrome step is skipped.\n" +
		"Install Google Chrome, or set REFETCH_CHROME to a Chrome binary."}
}

// claudeEnv reads Claude Code's settings and plugin files.
type claudeEnv struct{ home, cwd string }

type hookGroup struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Command string `json:"command"`
	} `json:"hooks"`
}

// line describes the group if it runs on WebFetch or fetch_page, or is
// empty.
func (g hookGroup) line(source, event string) string {
	var on []string
	for _, tool := range []string{toolWebFetch, toolFetchPage} {
		if matches(g.Matcher, tool) {
			on = append(on, tool)
		}
	}
	if len(on) == 0 {
		return ""
	}
	line := fmt.Sprintf("%s: %s on %s", source, event, strings.Join(on, ", "))
	if strings.Contains(source, "context-mode") && event == "PreToolUse" {
		return line + " (denies WebFetch; refetch watches ctx_fetch_and_index instead)"
	}
	for _, h := range g.Hooks {
		if strings.Contains(h.Command, "rtk ") {
			return line + " (RTK)"
		}
	}
	return line
}

type settingsFile struct {
	Permissions struct {
		Allow []string `json:"allow"`
	} `json:"permissions"`
	Hooks          map[string][]hookGroup `json:"hooks"`
	EnabledPlugins map[string]bool        `json:"enabledPlugins"`
}

func (e claudeEnv) settingsPaths() []string {
	return []string{
		filepath.Join(e.home, ".claude", "settings.json"),
		filepath.Join(e.home, ".claude", "settings.local.json"),
		filepath.Join(e.cwd, ".claude", "settings.json"),
		filepath.Join(e.cwd, ".claude", "settings.local.json"),
	}
}

func (e claudeEnv) settings() map[string]settingsFile {
	out := map[string]settingsFile{}
	for _, p := range e.settingsPaths() {
		var s settingsFile
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &s) == nil {
			out[p] = s
		}
	}
	return out
}

func (e claudeEnv) permissionCheck() check {
	allowed := map[string]bool{
		toolFetchPage:                    true,
		"mcp__plugin_refetch_refetch":    true,
		"mcp__plugin_refetch_refetch__*": true,
	}
	for path, s := range e.settings() {
		for _, rule := range s.Permissions.Allow {
			if allowed[rule] {
				return check{true, "permission", "fetch_page is allowed in " + path}
			}
		}
	}
	return check{false, "permission", "fetch_page is not allowed, so every rescue waits on a prompt (and fails under claude -p).\n" +
		`Add to ~/.claude/settings.json: "permissions": {"allow": ["` + toolFetchPage + `"]}`}
}

// hooksCheck lists other hooks that also run on WebFetch or refetch's tool.
func (e claudeEnv) hooksCheck() check {
	var found []string
	add := func(source string, hooks map[string][]hookGroup) {
		for _, ev := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
			for _, g := range hooks[ev] {
				if line := g.line(source, ev); line != "" {
					found = append(found, line)
				}
			}
		}
	}
	settings := e.settings()
	enabled := map[string]bool{}
	for _, p := range slices.Sorted(maps.Keys(settings)) {
		add(strings.Replace(p, e.home, "~", 1), settings[p].Hooks)
		maps.Copy(enabled, settings[p].EnabledPlugins)
	}
	for name, root := range e.plugins() {
		if !enabled[name] || strings.HasPrefix(name, "refetch@") {
			continue
		}
		var hf struct {
			Hooks map[string][]hookGroup `json:"hooks"`
		}
		if b, err := os.ReadFile(filepath.Join(root, "hooks", "hooks.json")); err == nil && json.Unmarshal(b, &hf) == nil {
			add("plugin "+name, hf.Hooks)
		}
	}
	slices.Sort(found)
	if len(found) == 0 {
		return check{true, "other hooks", "none on WebFetch or fetch_page"}
	}
	return check{true, "other hooks", strings.Join(found, "\n")}
}

// plugins maps each installed plugin that applies here (user scope, or
// project scope for this directory) to its install path.
func (e claudeEnv) plugins() map[string]string {
	var f struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			ProjectPath string `json:"projectPath"`
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(e.home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil || json.Unmarshal(b, &f) != nil {
		return out
	}
	for name, entries := range f.Plugins {
		for _, en := range entries {
			if en.Scope == "user" || en.ProjectPath == e.cwd {
				out[name] = en.InstallPath
				break
			}
		}
	}
	return out
}

// matches reports whether a hook matcher applies to tool: empty and "*"
// match everything, anything else is a regular expression for the whole
// name.
func matches(matcher, tool string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	re, err := regexp.Compile("^(?:" + matcher + ")$")
	if err != nil {
		return matcher == tool
	}
	return re.MatchString(tool)
}
