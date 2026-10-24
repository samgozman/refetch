package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// TestRunAs hands a profile to this test's own user, the one hand-over a
// test can make without root.
func TestRunAs(t *testing.T) {
	if uid, gid := nobody(); uid == 0 || gid == 0 {
		t.Errorf("nobody is %d:%d", uid, gid)
	}
	profile := t.TempDir()
	if err := os.MkdirAll(filepath.Join(profile, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	cmd := exec.Command("chrome")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	if err := runAs(cmd, profile, uid, gid); err != nil {
		t.Fatal(err)
	}
	if c := cmd.SysProcAttr.Credential; c == nil || c.Uid != uid || c.Gid != gid || c.NoSetGroups || len(c.Groups) != 0 {
		t.Errorf("credential %+v, want %d:%d with no supplementary groups", c, uid, gid)
	}
	if !slices.Contains(cmd.Env, "HOME="+profile) {
		t.Errorf("HOME is not the profile: %v", cmd.Env)
	}
	if err := runAs(cmd, filepath.Join(profile, "missing"), uid, gid); err == nil {
		t.Error("a missing profile was handed over")
	}
}
