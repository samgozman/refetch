//go:build !linux

package render

import "syscall"

// sysProcAttr puts Chrome in its own process group, so a timeout kills its
// helpers too.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
