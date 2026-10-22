//go:build e2e

package fetch

// e2eLocal is true only in the e2e build (go build -tags e2e), which reads
// the compatibility test's local server: loopback addresses and its
// self-signed certificate. Release builds compile this out.
const e2eLocal = true
