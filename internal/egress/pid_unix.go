//go:build !windows

package egress

import (
	"errors"
	"syscall"
)

// pidAlive reports whether a process with this id exists. EPERM means it does,
// under another user.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
