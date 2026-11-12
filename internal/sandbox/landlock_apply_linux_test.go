//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"golang.org/x/sys/unix"
)

// onThrowawayThread runs fn on an OS thread that is never handed back to the
// Go scheduler. applyLandlock restricts the calling thread for good, so it
// must not run on a thread other tests' goroutines (or the runtime's own
// subprocess spawns) are later scheduled on: the goroutine locks its thread,
// never unlocks it, and the runtime terminates the thread when it exits.
func onThrowawayThread(fn func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		// No UnlockOSThread: the restricted thread dies with this goroutine.
		done <- fn()
	}()
	return <-done
}

// applyLandlock is the syscall layer the helper runs: build the ruleset,
// add the plan's rules, and restrict this thread to them. It is covered here
// directly because the helper's calls land in a subprocess, outside the test
// binary's coverage map.
func TestApplyLandlockRestrictsTheCallingThread(t *testing.T) {
	withLandlock(t)
	granted := t.TempDir()
	// A file outside the plan, written before the restriction lands.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := onThrowawayThread(func() error {
		if err := applyLandlock(llPlan{
			Rules: []llRule{{Path: granted, Access: llRW(4)}},
			Net:   false,
		}, 4); err != nil {
			return fmt.Errorf("applyLandlock: %w", err)
		}
		if err := os.WriteFile(filepath.Join(granted, "x"), []byte("x"), 0o600); err != nil {
			return fmt.Errorf("the granted path is not writable: %w", err)
		}
		if _, err := os.ReadFile(outside); !errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("a path outside the plan was readable (err = %v)", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The test's own thread was never restricted.
	if _, err := os.ReadFile(outside); err != nil {
		t.Fatalf("the restriction leaked out of its thread: %v", err)
	}
}

// A missing path's rule is skipped when applied, like bubblewrap's --bind-try:
// the rest of the plan still lands.
func TestApplyLandlockSkipsARuleForAMissingPath(t *testing.T) {
	withLandlock(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	err := onThrowawayThread(func() error {
		if err := applyLandlock(llPlan{
			Rules: []llRule{{Path: missing, Access: llRW(4)}},
			Net:   false,
		}, 4); err != nil {
			return fmt.Errorf("applyLandlock: %w", err)
		}
		nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
		if err != nil {
			return fmt.Errorf("PR_GET_NO_NEW_PRIVS: %w", err)
		}
		if nnp != 1 {
			return fmt.Errorf("no_new_privs = %d, want 1", nnp)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Status must read "on" once a backend works here: this test makes one itself
// (withLandlock) rather than trusting the machine to already have one.
func TestStatusIsOnWhenTheTestMadeTheBackend(t *testing.T) {
	withLandlock(t)
	if got := Status(&config.SandboxSettings{Mode: ModeRequired}, posture.Defaults()); got != "on" {
		t.Fatalf("required mode with a backend the test set up: %q", got)
	}
}

// A kernel without Landlock must fail the syscall layer cleanly, not silently
// proceed: the ruleset cannot be created and nothing is restricted. The
// success-path tests above skip here, so this is what runs on a kernel like
// this one.
func TestApplyLandlockFailsCleanlyWithoutTheSyscall(t *testing.T) {
	if _, err := landlockABI(); err == nil {
		t.Skip("this kernel offers Landlock; the success-path tests cover it")
	}
	dir := t.TempDir()
	err := onThrowawayThread(func() error {
		return applyLandlock(llPlan{Rules: []llRule{{Path: dir, Access: llRW(4)}}}, 4)
	})
	if err == nil {
		t.Fatal("a kernel without Landlock applied a ruleset")
	}
	if !strings.Contains(err.Error(), "landlock ruleset") {
		t.Fatalf("err = %v, want the ruleset failure", err)
	}
}
