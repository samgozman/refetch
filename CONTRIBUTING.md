# Contributing to refetch

Thanks for helping. Bug reports, sites refetch cannot read, docs fixes and
code are all welcome.

## Reporting a blocked site or a bug

Open an issue with the bug report form. The most useful details:

- the output of `refetch doctor`
- the output of `refetch get <url>` for the page, which prints every step
  and its outcome
- your Claude Code version (`claude --version`) and OS

Some sites refuse every automated reader. refetch does not try to defeat
them at any cost, so "site X still refuses" may be the expected result. It
is still worth reporting, because the stats and detection can improve.

Report security issues privately, as described in [SECURITY.md](SECURITY.md).

## Setup

You need Go 1.27 and, for linting,
[golangci-lint](https://golangci-lint.run) v2.15.0. Chrome or Chromium is
optional; without it, the headless Chrome tests are skipped.

```sh
git clone https://github.com/samgozman/refetch
cd refetch
make build test
```

| Target | What it does |
| --- | --- |
| `make build` | Builds `build/refetch` |
| `make test` | Runs the tests with the race detector. They never touch the network |
| `make cover-check` | Runs the tests with coverage and fails below 85% |
| `make lint-build` | Builds `build/custom-gcl`: golangci-lint with the [maxcomments](https://github.com/samgozman/maxcomments-lint) plugin |
| `make lint` | `go mod tidy -diff` and the linters (builds `build/custom-gcl` when it is missing) |
| `make fmt` | Formats the code |
| `make notices` | Regenerates `THIRD_PARTY_NOTICES` after a dependency change |

[AGENTS.md](AGENTS.md) describes the layout and conventions in short form.

## Trying a change in Claude Code

Load the plugin from your checkout and point it at your build:

```sh
make build
REFETCH_BIN="$PWD/build/refetch" claude --plugin-dir .
```

To keep your real stats clean, also set `REFETCH_DATA` to a scratch
directory. `refetch doctor` shows what the session sees.

The compatibility run (`e2e/run.py`) runs real `claude -p` sessions against
a local test site, with and without RTK and context-mode. It costs about 20
sessions, so run it for changes to the hooks, detection or fetch steps, not
for every PR.

## Rules the code keeps

- Every connection goes through the public-only dialer, headless Chrome
  included. A change that adds a network path without it will not be
  merged.
- Hooks and the MCP server never break a session: failures become tool
  errors, or the hook exits 0 with no output.
- refetch never denies or rewrites another tool call, and never writes to
  Claude Code's settings.
- Tests use fakes and loopback servers only, never the internet.
- New behavior comes with a test. Keep coverage at 85% or above.

## Pull requests

- Keep each PR to one change, and describe what it fixes and how you
  checked it.
- Use [conventional commits](https://www.conventionalcommits.org) for
  commit messages and the PR title (`feat:`, `fix:`, `docs:`, `test:`,
  `ci:`, `chore:`). Release notes and version numbers come from them.
- Before pushing, run `make lint cover-check` and
  `claude plugin validate . --strict`. CI runs the same checks.
- After a dependency change, run `make notices` and commit
  `THIRD_PARTY_NOTICES`.
- Update the README when user-facing behavior or settings change.
- Leave `version` in `.claude-plugin/plugin.json` at `0.0.0` and the sha256
  table in `bin/refetch` empty. The release job writes both on the `release`
  branch, and a test fails if `main` carries either.

## Releases (maintainers)

release-please keeps a release PR open from the conventional commits on
`main`. Merging it tags the version and creates the GitHub release. The same
workflow then builds the binaries into that release, with build provenance
attestations. Last, it commits the new version and checksums onto the tag
and force-pushes that to the `release` branch. The marketplace installs the
plugin from `release`, so users only ever get released plugin files, and a
new version string is what Claude Code sees as an update.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
