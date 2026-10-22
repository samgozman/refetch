package render

import "syscall"

// sysProcAttr puts Chrome in its own process group, so a timeout kills its
// helpers too, and has the kernel kill it if refetch itself dies.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
