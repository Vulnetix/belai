//go:build windows

package localinfer

import (
	"os"
	"os/exec"
	"sync"

	"github.com/vulnetix/belai/internal/activity"
)

// makeStop returns a function that terminates cmd on Windows. Process groups
// are not supported, so the stop is a single-process kill.
func makeStop(cmd *exec.Cmd, handle *activity.Handle, pidfile string) func() error {
	stopped := false
	var mu sync.Mutex
	return func() error {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return nil
		}
		stopped = true
		if cmd.ProcessState != nil {
			if handle != nil {
				handle.Finish(cmd.ProcessState.ExitCode(), false, nil)
			}
			if pidfile != "" {
				_ = os.Remove(pidfile)
			}
			return nil
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		if handle != nil && cmd.ProcessState != nil {
			handle.Finish(cmd.ProcessState.ExitCode(), false, nil)
		}
		if pidfile != "" {
			_ = os.Remove(pidfile)
		}
		return nil
	}
}
