package render

import (
	"io/fs"
	"os"
	"os/exec"
	"os/user"
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

// runAsNobody runs Chrome as nobody, which keeps its sandbox on when
// refetch runs as root (in a container).
func runAsNobody(cmd *exec.Cmd, profile string) error {
	uid, gid := nobody()
	return runAs(cmd, profile, uid, gid)
}

// nobody is the nobody user's uid and gid, 65534 where it has no entry.
func nobody() (uid, gid uint32) {
	uid, gid = 65534, 65534
	if u, err := user.Lookup("nobody"); err == nil {
		if n, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
			uid = uint32(n)
		}
		if n, err := strconv.ParseUint(u.Gid, 10, 32); err == nil {
			gid = uint32(n)
		}
	}
	return uid, gid
}

// runAs hands the profile, as its home, to the user cmd runs as.
func runAs(cmd *exec.Cmd, profile string, uid, gid uint32) error {
	// os.Root keeps the walk inside the profile, even if a link appears.
	root, err := os.OpenRoot(profile)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	err = fs.WalkDir(root.FS(), ".", func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return root.Lchown(p, int(uid), int(gid))
	})
	if err != nil {
		return err
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid} // no supplementary groups: root's would stay otherwise
	cmd.Env = append(os.Environ(), "HOME="+profile, "XDG_CONFIG_HOME="+profile, "XDG_CACHE_HOME="+profile)
	return nil
}
