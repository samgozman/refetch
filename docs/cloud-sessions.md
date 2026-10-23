# refetch in cloud sessions

Cloud sessions (Claude Code on the web, the Code tab in the Claude app, and
`claude --cloud`) run on a fresh VM, and they don't install plugins, not even
the ones a repository's `.claude/settings.json` turns on. They do read a
repository's `.mcp.json` and the hooks in its `.claude/settings.json`, and
they run the environment's setup script. refetch runs there as a project's
MCP server and hooks instead of as a plugin.

If you drive a session on your own machine from your phone (Remote Control),
none of this applies: the plugin runs on your machine as usual.

## What works, and what doesn't

- **The Chrome-like client works.** Every connection goes through the
  session's proxy as a tunnel. For most sites the proxy passes TLS through
  untouched, so the site sees refetch's Chrome handshake. For the hosts the
  proxy decrypts to add credentials (GitHub, for example), the site sees the
  proxy's handshake instead.
- **Headless Chrome works.** The VM comes with Playwright's Chromium in
  `/opt/pw-browsers`, and refetch finds it there. The VM runs everything as
  root, so refetch runs Chrome as the `nobody` user, with Chrome's sandbox
  on.
- **Sites see a data center IP address.** Bot filters trust these less than
  a home connection, so more pages stay blocked than on your own machine.
- **The default network access blocks most sites.** The environment's
  default level, **Trusted**, allows package registries and a list of
  developer sites. refetch can only read what the level allows.

## Set it up

1. **Network access.** Edit the cloud environment, and set **Network
   access** to **Custom** with the domains you want Claude to read. Check
   **Also include default list of common package managers** to keep the
   default list.

2. **Setup script.** Add this to the environment's setup script. It installs
   the latest release's plugin files in `/opt/refetch` and downloads the
   binary for them, checked against the sha256 values pinned in those files.
   The environment cache keeps both, so new sessions don't download again.

   ```bash
   #!/bin/bash
   git clone --quiet --depth 1 --branch release https://github.com/samgozman/refetch /opt/refetch &&
     /opt/refetch/bin/refetch version || true
   ```

   To update refetch, rebuild the environment cache, or replace `--branch
   release` with a commit of the `release` branch to stay on one version.

3. **`.mcp.json`** at the repository root:

   ```json
   {
     "mcpServers": {
       "refetch": { "command": "/opt/refetch/bin/refetch", "args": ["mcp"] }
     }
   }
   ```

4. **`.claude/settings.json`** in the repository: the permission, the MCP
   server and the hooks. Each hook does nothing where `/opt/refetch` is
   missing, so teammates who use the plugin on their own machines don't get
   refetch's hooks twice.

   ```json
   {
     "permissions": { "allow": ["mcp__refetch__fetch_page"] },
     "enabledMcpjsonServers": ["refetch"],
     "hooks": {
       "SessionStart": [
         { "hooks": [{ "type": "command", "command": "[ -x /opt/refetch/bin/refetch ] && /opt/refetch/bin/refetch hook SessionStart || true", "timeout": 120 }] }
       ],
       "PreToolUse": [
         { "matcher": "WebFetch", "hooks": [{ "type": "command", "command": "[ -x /opt/refetch/bin/refetch ] && /opt/refetch/bin/refetch hook PreToolUse || true", "timeout": 10 }] }
       ],
       "PostToolUse": [
         { "matcher": "WebFetch|mcp__refetch__.*", "hooks": [{ "type": "command", "command": "[ -x /opt/refetch/bin/refetch ] && /opt/refetch/bin/refetch hook PostToolUse || true", "timeout": 90 }] }
       ],
       "PostToolUseFailure": [
         { "matcher": "WebFetch|mcp__refetch__.*", "hooks": [{ "type": "command", "command": "[ -x /opt/refetch/bin/refetch ] && /opt/refetch/bin/refetch hook PostToolUseFailure || true", "timeout": 90 }] }
       ]
     }
   }
   ```

   Merge these keys into the file if it already exists. On your own machine,
   Claude Code lists the `refetch` project server as failed, since
   `/opt/refetch` isn't there; the plugin's own server is the one that runs.

5. **Check it.** In a cloud session, ask Claude to run
   `/opt/refetch/bin/refetch doctor`. The `network` line says whether the
   environment's network access lets refetch reach sites beyond the default
   list.

## How refetch uses the proxy

In a cloud session (`CLAUDE_CODE_REMOTE=true`), refetch sends every
connection through the proxy in `HTTPS_PROXY` as a CONNECT tunnel, except to
the hosts `NO_PROXY` lists. On your own machine it ignores `HTTPS_PROXY`
unless you set `use_proxy = true`, because a proxy that decrypts TLS would
replace refetch's Chrome handshake with its own.

The proxy resolves host names itself, so refetch checks what it can before
asking it: an IP address must be public, and a name that resolves on the VM
must resolve only to public addresses. A name the VM can't resolve is left
to the proxy, which then decides what the address may be. See
[SECURITY.md](../SECURITY.md).
