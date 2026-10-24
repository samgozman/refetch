package render

import (
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// sysProcAttr puts Chrome in its own process group, so a timeout kills its
// helpers too, and has the kernel kill it if refetch itself dies.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// canDropRoot is true where a root refetch can run Chrome as another user.
const canDropRoot = true

// runAsNobody runs Chrome as the unprivileged nobody user, which keeps
// Chrome's sandbox on when refetch runs as root (in a container). The
// throwaway profile becomes nobody's, and Chrome's home with it.
func runAsNobody(cmd *exec.Cmd, profile string) error {
	uid, gid := uint32(65534), uint32(65534)
	if u, err := user.Lookup("nobody"); err == nil {
		if n, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
			uid = uint32(n)
		}
		if n, err := strconv.ParseUint(u.Gid, 10, 32); err == nil {
			gid = uint32(n)
		}
	}
	err := filepath.WalkDir(profile, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, int(uid), int(gid))
	})
	if err != nil {
		return err
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid, NoSetGroups: true}
	cmd.Env = append(os.Environ(), "HOME="+profile, "XDG_CONFIG_HOME="+profile, "XDG_CACHE_HOME="+profile)
	return nil
}
