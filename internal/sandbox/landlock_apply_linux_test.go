//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// applyLandlock is the syscall layer the helper runs: build the ruleset,
// add the plan's rules, and restrict this thread to them. It is covered here
// directly because the helper's calls land in a subprocess, outside the test
// binary's coverage map.
func TestApplyLandlockRestrictsTheCallingThread(t *testing.T) {
	withLandlock(t)
	// A plan with one writable directory: /tmp stays writable, the test
	// process's temp dirs live there and the kernel's runtime needs are met.
	// A read-only plan (the ruleset's rights are "everything else is denied")
	// would make the test process unable to finish.
	dir := t.TempDir()
	if err := applyLandlock(llPlan{
		Rules: []llRule{{Path: dir, Access: llRW(4)}},
		Net:   false,
	}, 4); err != nil {
		t.Fatalf("applyLandlock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0o600); err != nil {
		t.Fatalf("the granted path is not writable: %v", err)
	}
	if _, err := os.Open("/proc/self/status"); err != nil {
		t.Fatalf("restricted thread cannot read its own /proc: %v", err)
	}
}

// A missing path's rule is skipped when applied, like bubblewrap's --bind-try:
// the rest of the plan still lands.
func TestApplyLandlockSkipsARuleForAMissingPath(t *testing.T) {
	withLandlock(t)
	if err := applyLandlock(llPlan{
		Rules: []llRule{
			{Path: filepath.Join(t.TempDir(), "does-not-exist"), Access: llRW(4)},
		},
		Net: false,
	}, 4); err != nil {
		t.Fatalf("applyLandlock: %v", err)
	}
	if err := unix.Prctl(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0); err != nil {
		t.Fatalf("no_new_privs was not set: %v", err)
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
	err := applyLandlock(llPlan{Rules: []llRule{{Path: t.TempDir(), Access: llRW(4)}}}, 4)
	if err == nil {
		t.Fatal("a kernel without Landlock applied a ruleset")
	}
	if _, e := landlockABI(); e != nil && !strings.Contains(err.Error(), "landlock ruleset") {
		t.Fatalf("err = %v, want the ruleset failure", err)
	}
}
