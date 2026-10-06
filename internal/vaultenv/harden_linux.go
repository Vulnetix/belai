//go:build linux

package vaultenv

import "syscall"

// HardenProcess marks this process non-dumpable, so a command it starts as the same
// user cannot read its memory through /proc/PID/mem or attach to it with ptrace.
// The vault's values are held in memory here between a lease and a tool call.
func HardenProcess() error {
	const prSetDumpable = 4
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
