//go:build !windows

// Package proc provides process-lifecycle helpers for subprocesses that may
// spawn deep child trees.
package proc

import (
	"os/exec"
	"syscall"
)

// SetProcessGroup puts the command in its own process group and arranges for
// cancellation to kill the whole group. This prevents orphaned grandchildren
// (e.g. scanners spawned by vulnetix scan) from outliving the parent when the
// context is cancelled or the timeout fires.
func SetProcessGroup(ec *exec.Cmd) {
	ec.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ec.Cancel = func() error {
		if ec.Process == nil {
			return nil
		}
		// The group id equals the leader's pid because of Setpgid above. Fall
		// back to the single process if the group is already gone.
		if err := syscall.Kill(-ec.Process.Pid, syscall.SIGKILL); err != nil {
			return ec.Process.Kill()
		}
		return nil
	}
}
