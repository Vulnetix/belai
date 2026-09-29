//go:build windows

package localinfer

import (
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/activity"
)

// makeStop returns a function that terminates cmd on Windows. Process groups
// are not supported, so the stop is a single-process kill. exited is closed
// by the goroutine that owns cmd.Wait.
func makeStop(cmd *exec.Cmd, exited <-chan struct{}, handle *activity.Handle, pidfile string) func() error {
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
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
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
