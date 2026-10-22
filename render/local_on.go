//go:build e2e

package render

// e2eLocal is true only in the e2e build (go build -tags e2e): Chrome then
// accepts the compatibility test server's self-signed certificate. Release
// builds compile this out.
const e2eLocal = true
