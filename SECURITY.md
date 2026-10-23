# Security policy

refetch fetches pages from the internet on Claude's behalf and runs headless
Chrome. The URL it fetches comes from Claude, so a web page or a prompt
injection can choose it. These guarantees are security boundaries:

- **Public addresses only.** No loopback, private, link-local, cloud
  metadata or other special-purpose addresses, and none of this machine's
  own interface addresses. The check runs on the resolved address of every
  connection and redirect, so DNS rebinding cannot get around it. Headless
  Chrome reaches the network only through a local proxy that applies the
  same check. Behind an upstream proxy (see below), the check runs before
  each connection instead.
- **Chrome never runs as root.** When refetch runs as root on Linux, Chrome
  runs as the `nobody` user, with its sandbox on; elsewhere the step does
  not run as root at all.
- **Only pinned binaries run.** The plugin shim downloads the binary for
  its version from this repo's GitHub release and checks the archive and the
  binary against the sha256 values pinned in the plugin. It checks the
  cached binary again on every run. It takes its own tools (`curl`, `tar`,
  `sha256sum`) from the system directories only.
- **Hooks never block.** refetch's hooks never deny, rewrite or block a tool
  call, and always exit 0.
- **Page text is marked untrusted.** `fetch_page` puts page text between two
  marker lines with a random nonce. A failure reason read from another
  tool's result reaches Claude as a fixed category (an HTTP status, a
  timeout), and other error text as one short line.
- **Local data stays local.** `events.db` (mode 0600) stores URLs without
  their query strings, fragments or user info, in its error text too.
  Nothing is sent anywhere.

## What you trust

- **The release pipeline.** The plugin users install comes from the
  `release` branch, which only the release workflow writes, after the
  binaries it points at are published. Each archive has a build provenance
  attestation (see the README).
- **Your environment.** `REFETCH_BIN` runs any binary you name, and
  `REFETCH_CONFIG`, `REFETCH_DATA`, `REFETCH_CHROME` and `XDG_CACHE_HOME`
  decide what runs and where data goes. A project's `.claude/settings.json`
  can set environment variables for hooks and MCP servers, so opening a
  project you don't trust is as risky with refetch as without it.
- **Pages' own scripts.** While headless Chrome renders a page, the page's
  JavaScript runs for up to `render_timeout` and can send requests to public
  hosts from your IP address.
- **The local proxy.** During a render, a proxy listens on a random port on
  127.0.0.1. Another process on your machine could use it to reach public
  addresses for those few seconds; it never reaches anything the address
  check refuses.
- **An upstream proxy, when one is used.** In cloud sessions, or with
  `use_proxy`, connections go through `HTTPS_PROXY`, and the proxy resolves
  host names itself. refetch refuses non-public IP addresses and names that
  resolve on this machine to non-public addresses, but a name that only the
  proxy can resolve is the proxy's to check. Hosts `NO_PROXY` lists are
  dialled directly, with the full check.
- **What the address check cannot see.** An internal network on public IP
  space, or a service that trusts your IP address, looks public.

## Reporting a vulnerability

If you find a way around any of the guarantees above, or another
vulnerability, please report it privately through GitHub's
[private vulnerability reporting](https://github.com/samgozman/refetch/security/advisories/new),
not as a public issue. Include the steps to reproduce and the refetch
version (`refetch version`).

You can expect a first reply within a week. Fixes go into the next release,
and the advisory is published once that release is out.

Only the latest release is supported.
