package render

import "syscall"

// osVersion is the macOS version, as Chrome names it in its client hints.
func osVersion() string {
	v, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return threeParts(v)
}
