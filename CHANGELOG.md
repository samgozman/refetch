# Changelog

## 0.1.0 (2026-10-22)


### Features

* Claude Code plugin with hooks, shim, doctor and hand-off ([c522da4](https://github.com/samgozman/refetch/commit/c522da44ee726bad1653ac0dd9b816920c365a36))
* **doctor:** warn when refetch is more than one minor version behind ([f68a72b](https://github.com/samgozman/refetch/commit/f68a72b51b1491977831ffb2460366179405044f))
* fetcher, MCP server, event store and stats report ([3e90411](https://github.com/samgozman/refetch/commit/3e904114e2c7d417b6db4cb352cb0da45489af0d))
* headless Chrome step for challenge and JavaScript-only pages ([cf1c3bd](https://github.com/samgozman/refetch/commit/cf1c3bd10377c20cfe9d37741657f9991753af97))
* **render:** render_timeout defaults to 15s ([70ca8ca](https://github.com/samgozman/refetch/commit/70ca8ca463bff588d23c47ae9193fa4bd1027c5f))


### Bug Fixes

* concurrent store creation, small pages, and the e2e compatibility run ([5c21004](https://github.com/samgozman/refetch/commit/5c210047618e982d309acd3892321dc58eedab1b))
* **deps:** circl 1.6.3 (GO-2026-4550) and compress 1.18.7 ([92c4aec](https://github.com/samgozman/refetch/commit/92c4aecaf180e42f9a1b4605507d18ed5cb01e80))
* detect AWS WAF 202 challenges, empty summaries and script-only pages ([1bfe14a](https://github.com/samgozman/refetch/commit/1bfe14a1473fec134124e1cb41f5fd818f81904d))
* **fetch:** refuse every special-purpose address, including embedded IPv4 ([ce1d748](https://github.com/samgozman/refetch/commit/ce1d7482bf9f6fb5632476e6d1d74f09ef72d5cb))
* **fetch:** refuse this machine's own addresses and Azure's WireServer ([d1e40a0](https://github.com/samgozman/refetch/commit/d1e40a09951fb02113c890003f60cc9c71a01af2))
* **fetch:** refuse zoned IPv6 addresses in the public-only check ([1e27efd](https://github.com/samgozman/refetch/commit/1e27efd7f20e1a6c2ce91cbb233a2ac162804d84))
* **hook:** ignore forged batch error lines and fence page text with a nonce ([d142764](https://github.com/samgozman/refetch/commit/d142764ccb2e6f650112d4d5538ae3cbbf493809))
* **hook:** quote batch failure reasons only as fixed categories ([5bc7659](https://github.com/samgozman/refetch/commit/5bc76590660594b44a435a4b166a19fb829224e2))
* install the plugin from a release branch and check the cached binary on every run ([d025a0a](https://github.com/samgozman/refetch/commit/d025a0add3d5c4fcffe87c96e66ab124b612cdd8))
* **render:** always kill Chrome's process group, close proxy tunnels, sweep stale profiles ([e889ce6](https://github.com/samgozman/refetch/commit/e889ce68e72a19b3885cd75a3f5a5b1352cc6a96))
* **render:** never run headless Chrome as root ([a7c6eba](https://github.com/samgozman/refetch/commit/a7c6ebaa6703fa88b2023be15b0c6ea0ef5ad1d8))
* **shim:** run a Homebrew refetch only from fixed Homebrew directories ([33ad3ff](https://github.com/samgozman/refetch/commit/33ad3ffd6e7607d5f3af05c100bf97bec086dc59))
* **stats:** count a denied WebFetch followed by fetch_page as a picked-up read ([e828783](https://github.com/samgozman/refetch/commit/e828783443eb9053f1bd93be14cc9f5d12fe0303))
* **store:** strip query strings from URLs in stored errors, events.db 0600 ([196b875](https://github.com/samgozman/refetch/commit/196b87550c76370764d725c6ddc0f6288581f957))
