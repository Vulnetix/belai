//go:build windows

package proc

import (
	"errors"
	"os"
	"os/exec"
)

// Alive reports whether a process with pid exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}

// ErrNoDetach is returned where detached workers are not supported.
var ErrNoDetach = errors.New("detached agents are not supported on this platform; run `belai agent run` in the foreground")

// Detach is not supported on this platform.
func Detach(cmd *exec.Cmd) error { return ErrNoDetach }

// DetachPID is not supported on this platform.
func DetachPID(cmd *exec.Cmd) (int, error) { return 0, ErrNoDetach }

// Terminate stops pid.
func Terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// Kill stops pid.
func Kill(pid int) error { return Terminate(pid) }
