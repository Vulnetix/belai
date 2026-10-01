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
