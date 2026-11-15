//go:build !darwin

package render

import (
	"os"
	"strings"
)

// osVersion is the kernel release, as Chrome on Linux names it in its
// client hints.
func osVersion() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return threeParts(strings.TrimSpace(string(b)))
}
