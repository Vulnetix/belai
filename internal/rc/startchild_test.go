//go:build !windows

package rc

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// startChild once failed every dispatched start with "exec: command with a
// non-nil Cancel was not created with CommandContext".
func TestStartChildSpawnsInOwnProcessGroup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-belai")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nsleep 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	pid, wait, err := startChild(Child{
		Exe: exe, Cwd: dir, Dispatch: "d1", SessionID: "s1", Idle: time.Minute,
		Prompt: "hello", LogPath: filepath.Join(dir, "sessions", "s1.log"),
	})
	if err != nil {
		t.Fatalf("startChild: %v", err)
	}
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid {
		t.Errorf("child pgid = %d (err %v), want its own group %d", pgid, err, pid)
	}
	if err := wait(); err != nil {
		t.Errorf("wait: %v", err)
	}
}

// The daemon's control flags reach every session as fixed argv, and only
// those two words: nothing from the request is added.
func TestStartChildPassesControlFlags(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-belai")
	out := filepath.Join(dir, "argv")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho \"$@\" > "+out+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		controls, off bool
		want          string
	}{
		{false, false, ""},
		{true, false, " -controls"},
		{true, true, " -controls -allow-guardrails-off"},
	} {
		_, wait, err := startChild(Child{Exe: exe, Cwd: dir, Dispatch: "d1", SessionID: "s1", Idle: time.Minute,
			Prompt: "hi", Controls: c.controls, GuardrailsOff: c.off})
		if err != nil {
			t.Fatal(err)
		}
		_ = wait()
		got, _ := os.ReadFile(out)
		want := "rc-session -dispatch d1 -session-id s1 -idle 1m0s" + c.want + "\n"
		if string(got) != want {
			t.Fatalf("argv = %q, want %q", got, want)
		}
	}
}
