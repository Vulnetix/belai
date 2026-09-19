//go:build windows

// Package proc provides process-lifecycle helpers for subprocesses that may
// spawn deep child trees.
package proc

import "os/exec"

// SetProcessGroup is a no-op on platforms without POSIX process groups;
// exec.CommandContext's default single-process kill applies.
func SetProcessGroup(ec *exec.Cmd) {}
