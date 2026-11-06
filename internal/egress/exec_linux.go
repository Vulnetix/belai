//go:build linux

package egress

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// dieWithParent kills this helper when the process that started it goes, the
// way bubblewrap's --die-with-parent does for what it runs.
func dieWithParent() error {
	ppid := os.Getppid()
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
		return err
	}
	if os.Getppid() != ppid {
		return errors.New("the parent is gone; nothing was run")
	}
	return nil
}

// childAttr makes the command die with the helper.
func childAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }
