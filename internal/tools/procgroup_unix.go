//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group and kills the
// whole group on cancellation.
//
// exec.CommandContext's default only kills the process it started. For a full
// mode command that is `sh`, so a pipeline's members, a backgrounded job, or
// anything sh exec'd into a child survives the timeout and keeps running —
// holding the output pipe open and, with WaitDelay, outliving the agent's
// interest in it. Signalling the negative pid reaches the group.
func setProcessGroup(ec *exec.Cmd) {
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
