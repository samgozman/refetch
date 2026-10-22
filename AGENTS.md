# AGENTS.md

Notes for AI coding agents (and people) working on refetch. Read
[CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow.

refetch is a Go 1.27 module, `github.com/samgozman/refetch`: one binary that
is the `fetch_page` MCP server, the Claude Code hook handler, the manual
fetcher and the stats report. The repo root is also the Claude Code plugin.

## Commands

- `make build`: `build/refetch` (`VERSION=x.y.z` sets the version string)
- `make test`: race-enabled unit tests, no network needed
- `make cover-check`: tests with coverage; fails below 85% (CI runs this)
- `make lint`: `go mod tidy -diff` and golangci-lint with the maxcomments
  plugin (`build/custom-gcl`, built from `.custom-gcl.yml` by
  `make lint-build`; needs golangci-lint v2.15.0 on PATH)
- `make notices`: regenerate `THIRD_PARTY_NOTICES` after any dependency change
- `claude plugin validate . --strict`: check the plugin manifests
- `REFETCH_DATA=/tmp/r REFETCH_CONFIG=/dev/null build/refetch get <url>`: try
  a fetch without touching your real data

## Layout

- `fetch/`: public package; the clients, the public-only dialer, challenge
  and JavaScript-only detection, HTML to text
- `render/`: the headless Chrome step: Chrome discovery, the local proxy on
  the public-only dialer, `--dump-dom` under `render_timeout`
- `store/`: SQLite event log (`tool_calls`, `attempts`, `daily` roll-up)
- `stats/`: the funnel and its text report
- `config/`: defaults, `config.toml`, `REFETCH_*` env vars
- `cmd/refetch/`: subcommands (`mcp`, `hook`, `get`, `stats`, `doctor`)
- `.claude-plugin/`, `hooks/hooks.json`, `skills/`: the plugin
- `bin/refetch`: the plugin's shell shim, not a build output. Builds go to
  `build/`.
- `e2e/`: the compatibility run against a local test site (`-tags e2e`)
- `scripts/`: notices and the release job's plugin-version script

## Conventions

- Every connection goes through the public-only dialer
  (`fetch.DialContext`); never add a client that bypasses it. Headless
  Chrome reaches the network only through `render`'s proxy on that dialer.
- Hooks and the MCP server must never break a session: report failures as
  tool errors, or exit 0 with empty output. Hooks finish in well under
  50 ms.
- refetch never denies or rewrites a tool call, and never writes to Claude
  Code's settings.
- Write modern Go: `errors.AsType`, `encoding/json/v2`, `t.Context()`,
  `slices`/`maps`.
- Tests use fake getters or loopback servers, never the live network.
- Comments explain why, not what; maxcomments caps comment lines per
  function and per file.
- Conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `ci:`,
  `chore:`). release-please builds the release notes and the version from
  them.
- Don't put a `CLAUDE.md` at the repo root: the root is the plugin, and the
  plugin validator flags it. Claude Code reads this file through
  `.claude/CLAUDE.md`.
- `version` in `.claude-plugin/plugin.json` and the sha256 table in
  `bin/refetch` are written by the release job; don't edit them by hand.
