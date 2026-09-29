//go:build !windows

package localinfer

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/vulnetix/belai/internal/activity"
)

// makeStop returns a function that gracefully terminates cmd's process group.
// exited is closed by the one goroutine that owns cmd.Wait, so stop waits on
// it instead of calling Wait a second time.
func makeStop(cmd *exec.Cmd, exited <-chan struct{}, handle *activity.Handle, pidfile string) func() error {
	if cmd.Process == nil {
		return func() error { return nil }
	}
	pid := cmd.Process.Pid
	stopped := false
	var mu sync.Mutex
	return func() error {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return nil
		}
		stopped = true
		select {
		case <-exited:
		default:
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(3 * time.Second):
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				<-exited
			}
		}
		if handle != nil {
			code := 0
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
			handle.Finish(code, false, nil)
		}
		if pidfile != "" {
			_ = os.Remove(pidfile)
		}
		return nil
	}
}
