# refetch

Go 1.27 module `github.com/samgozman/refetch`: one binary that is the
`fetch_page` MCP server, the manual fetcher and the stats report.

## Commands

- `make build`: `build/refetch` (`VERSION=x.y.z` sets the version string)
- `make test`: race-enabled unit tests, no network needed
- `make lint`: `go mod tidy -diff` and golangci-lint (`.golangci.yml`)
- `make notices`: regenerate `THIRD_PARTY_NOTICES` after any dependency change
- `REFETCH_DATA=/tmp/r REFETCH_CONFIG=/dev/null build/refetch get <url>`: try a fetch without touching your real data

## Layout

- `fetch/`: public package; the clients, the public-only dialer, challenge detection, HTML to text
- `store/`: SQLite event log (`tool_calls`, `attempts`, `daily` roll-up)
- `stats/`: the funnel and its text report
- `config/`: defaults, `config.toml`, `REFETCH_*` env vars
- `cmd/refetch/`: subcommands

## Conventions

- Every connection goes through the public-only dialer; never add a client that bypasses it.
- Hooks and the MCP server must never break a session: report failures as tool errors or exit 0.
- Write modern Go: `errors.AsType`, `encoding/json/v2`, `t.Context()`, `slices`/`maps`.
- Tests use fake getters or loopback servers, never the live network.
- Conventional commits (`feat:`, `fix:`, `chore:`); release notes come from them.
