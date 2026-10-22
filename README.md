# refetch

Reads web pages that Claude Code's WebFetch could not: blocked, challenged or
timed out. It fetches with a Chrome TLS fingerprint, reaches public addresses
only, reads one page per call and keeps no cookies. Local stats show how often
it helped.

Status: early development. The Claude Code plugin, install steps and full
documentation come with the first release.

## Try it from source

```sh
make build
build/refetch get https://example.com
claude mcp add refetch -- "$PWD/build/refetch" mcp
build/refetch stats
```

## License

MIT. refetch includes [tls-client](https://github.com/bogdanfinn/tls-client)
by Bogdan Finn (BSD-4-Clause); this product includes software developed by
Bogdan Finn. See `THIRD_PARTY_NOTICES` for every dependency's license.
