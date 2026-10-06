//go:build windows

package bgproc

import (
	"fmt"
	"os"
	"os/exec"
)

func (r *specRun) errUser() error {
	return fmt.Errorf("the process runs as user %q, which this platform cannot do, so it was not started", r.user)
}

// checkUser refuses a process that names a user: running as another account is
// not supported here, and it never runs as the current one instead.
func (r *specRun) checkUser() error {
	if r.user == "" {
		return nil
	}
	return r.errUser()
}

func (r *specRun) applyUser(ec *exec.Cmd) error { return r.checkUser() }

func chownToUser(f *os.File, name string) error {
	return fmt.Errorf("user %q is not supported on this platform", name)
}
