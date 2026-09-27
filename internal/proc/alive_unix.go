//go:build !windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Alive reports whether a process with pid exists. Signal 0 checks for the
// process without delivering anything; EPERM means it exists but belongs to
// another user, which still counts as alive.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Detach starts cmd in its own session, so it outlives the process that
// started it and a terminal hang-up in the parent never reaches it. stdin is
// /dev/null; stdout and stderr are whatever the caller set (a log file).
// The child is released: the caller does not wait for it.
func Detach(cmd *exec.Cmd) error {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Terminate asks pid's process group to stop (SIGTERM), falling back to the
// process alone when it leads no group of its own.
func Terminate(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// Kill ends pid's process group at once (SIGKILL).
func Kill(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
