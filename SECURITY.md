# Security policy

refetch fetches pages from the internet on Claude's behalf and runs headless
Chrome, so a few of its guarantees are security boundaries:

- It connects to public addresses only: no loopback, private networks, link
  local or cloud metadata addresses, on every connection and redirect,
  headless Chrome included.
- The plugin shim runs only a binary whose sha256 matches the table pinned
  in the plugin, or an exact-version Homebrew binary in a fixed directory.
- Hooks never deny, rewrite or block another tool call.
- Page text reaches Claude marked as untrusted.

If you find a way around any of these, or another vulnerability, please
report it privately. Use GitHub's
[private vulnerability reporting](https://github.com/samgozman/refetch/security/advisories/new)
rather than a public issue. Include the steps to reproduce and the refetch
version (`refetch version`).

You can expect a first reply within a week. Fixes go into the next release,
and the advisory is published once that release is out.

Only the latest release is supported.
