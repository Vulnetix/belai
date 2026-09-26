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
func makeStop(cmd *exec.Cmd, handle *activity.Handle, pidfile string) func() error {
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
		// If the process already exited, Wait has been called and
		// ProcessState is populated. Re-waiting races and is unnecessary.
		if cmd.ProcessState != nil {
			if handle != nil {
				handle.Finish(cmd.ProcessState.ExitCode(), false, nil)
			}
			if pidfile != "" {
				_ = os.Remove(pidfile)
			}
			return nil
		}
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			<-done
		}
		if handle != nil {
			handle.Finish(cmd.ProcessState.ExitCode(), false, nil)
		}
		if pidfile != "" {
			_ = os.Remove(pidfile)
		}
		return nil
	}
}
