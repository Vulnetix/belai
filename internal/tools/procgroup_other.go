//go:build windows

package tools

import "os/exec"

// setProcessGroup is a no-op on platforms without POSIX process groups;
// exec.CommandContext's default single-process kill applies.
func setProcessGroup(ec *exec.Cmd) {}
