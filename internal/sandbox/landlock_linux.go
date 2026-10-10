//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// landlockABI asks the kernel which Landlock ABI it offers, or says why none.
func landlockABI() (int, error) {
	r, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if e != 0 {
		return 0, landlockMissing(e)
	}
	return int(r), nil
}

// landlockMissing words the errno of a kernel without usable Landlock.
func landlockMissing(e syscall.Errno) error {
	switch e {
	case unix.ENOSYS:
		return errors.New("not in this kernel (needs Linux 5.13+)")
	case unix.EOPNOTSUPP:
		return errors.New("disabled: not in the kernel's lsm list")
	case unix.EPERM:
		return errors.New("blocked by seccomp (Docker: --security-opt seccomp=unconfined or a profile that allows landlock_*)")
	}
	return e
}

// landlockProbe runs this executable as the helper with -probe and reads the
// ABI it prints. It is a subprocess, not a syscall here, so that a process
// whose own binary cannot act as the helper (a test binary without the hook)
// finds no backend rather than one it cannot use.
func landlockProbe(exe string) (int, error) {
	out, err := exec.Command(exe, LandlockCommand, "-probe").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return 0, errors.New(strings.TrimPrefix(msg, "landlock: "))
	}
	s := strings.TrimSpace(string(out))
	abi, convErr := strconv.Atoi(strings.TrimPrefix(s, "abi="))
	if !strings.HasPrefix(s, "abi=") || convErr != nil {
		return 0, fmt.Errorf("the probe printed %q", s)
	}
	return abi, nil
}

// applyLandlock builds the ruleset and restricts this thread with it. From
// here on the thread and everything it starts have only the plan's rights.
func applyLandlock(plan llPlan, abi int) error {
	attr := unix.LandlockRulesetAttr{Access_fs: llHandledFS(abi)}
	if plan.Net {
		attr.Access_net = llNetBindTCP | llNetConnectTCP
	}
	fd, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if e != 0 {
		return fmt.Errorf("landlock ruleset: %v", landlockMissing(e))
	}
	defer unix.Close(int(fd))
	for _, r := range plan.Rules {
		pfd, err := unix.Open(r.Path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			// Like bubblewrap's --bind-try: a path that is not there is nothing.
			continue
		}
		if err != nil {
			return fmt.Errorf("landlock rule for %s: %v", r.Path, err)
		}
		pb := unix.LandlockPathBeneathAttr{Allowed_access: r.Access, Parent_fd: int32(pfd)}
		_, _, e := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, fd, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&pb)), 0, 0, 0)
		unix.Close(pfd)
		if e != 0 {
			return fmt.Errorf("landlock rule for %s: %v", r.Path, e)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %v", err)
	}
	if _, _, e := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); e != 0 {
		return fmt.Errorf("landlock restrict: %v", e)
	}
	return nil
}

// dieWithParent is bubblewrap's --die-with-parent: the command is killed when
// Belai goes. The parent is checked again after the request, which cannot
// catch a parent that went before it.
func dieWithParent() error {
	ppid := os.Getppid()
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
		return fmt.Errorf("pdeathsig: %v", err)
	}
	if os.Getppid() != ppid {
		return errors.New("the parent is gone; nothing was run")
	}
	return nil
}

// execProcess replaces this process with argv, found on PATH when it has no
// directory.
func execProcess(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}
