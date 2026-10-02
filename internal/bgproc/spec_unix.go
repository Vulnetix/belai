//go:build !windows

package bgproc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// runAs is the account a process runs as.
type runAs struct {
	u      *user.User
	uid    uint32
	gid    uint32
	groups []uint32
}

// lookupRunAs resolves the account a document names. Only root can run a process
// as another user, so anything else is refused here: a process that names a user
// never runs as the account Belai runs as in place of it.
func lookupRunAs(name string) (runAs, error) {
	if os.Geteuid() != 0 {
		return runAs{}, fmt.Errorf("the process runs as user %q, and Belai is not running as root, so it was not started (it never runs as another user)", name)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return runAs{}, fmt.Errorf("the process runs as user %q, who does not exist on this host", name)
	}
	uid, err1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
	if err1 != nil || err2 != nil {
		return runAs{}, fmt.Errorf("user %q has a non-numeric id", name)
	}
	ra := runAs{u: u, uid: uint32(uid), gid: uint32(gid)}
	if ids, err := u.GroupIds(); err == nil {
		for _, g := range ids {
			if n, err := strconv.ParseUint(g, 10, 32); err == nil {
				ra.groups = append(ra.groups, uint32(n))
			}
		}
	}
	return ra, nil
}

// checkUser refuses a start whose user cannot be honoured, before anything is opened.
func (r *specRun) checkUser() error {
	if r.user == "" {
		return nil
	}
	_, err := lookupRunAs(r.user)
	return err
}

// applyUser makes the command run as the document's user, with that account's
// HOME, USER and LOGNAME unless the document sets them. It must run after the
// process group is set, which replaces the command's SysProcAttr.
func (r *specRun) applyUser(ec *exec.Cmd) error {
	if r.user == "" {
		return nil
	}
	ra, err := lookupRunAs(r.user)
	if err != nil {
		return err
	}
	if ec.SysProcAttr == nil {
		ec.SysProcAttr = &syscall.SysProcAttr{}
	}
	ec.SysProcAttr.Credential = &syscall.Credential{Uid: ra.uid, Gid: ra.gid, Groups: ra.groups}
	for k, v := range map[string]string{"HOME": ra.u.HomeDir, "USER": ra.u.Username, "LOGNAME": ra.u.Username} {
		if _, set := r.doc.Env[k]; !set && v != "" {
			ec.Env = setEnv(ec.Env, k, v)
		}
	}
	return nil
}

func setEnv(env []string, k, v string) []string {
	for i, kv := range env {
		if strings.HasPrefix(kv, k+"=") {
			env[i] = k + "=" + v
			return env
		}
	}
	return append(env, k+"="+v)
}

// chownToUser hands a redirect file to the account the process runs as, so a
// process started as root for another user can write its own output.
func chownToUser(f *os.File, name string) error {
	ra, err := lookupRunAs(name)
	if err != nil {
		return errors.New(err.Error())
	}
	return f.Chown(int(ra.uid), int(ra.gid))
}
