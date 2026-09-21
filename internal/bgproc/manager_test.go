package bgproc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)
	return NewManager(dir, run.Config{}, nil, config.Settings{}, posture.Defaults(), tools.Capabilities{})
}

func TestStartAndStop(t *testing.T) {
	m := testManager(t)
	p, err := m.Start("sleep", "sleep 30")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if p.State != StateRunning {
		t.Fatalf("state = %q, want running", p.State)
	}
	if err := m.Stop(p.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, ok := m.Lookup(p.ID); ok {
		t.Fatal("stopped process should be removed")
	}
}

func TestExitFiresRecover(t *testing.T) {
	m := testManager(t)
	// Use a command that exits quickly with a distinct code.
	p, err := m.Start("false", "false")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Wait for exit + handleExit dispatch.
	time.Sleep(300 * time.Millisecond)

	_ = p // referenced only for the handle
	sawRecover := false
	drainEvents(m, func(e Event) bool {
		if e.Kind == "recover" {
			sawRecover = true
		}
		return sawRecover
	})
	if !sawRecover {
		t.Fatal("expected a recover event after unexpected exit")
	}
}

func TestStoppedByUserDoesNotRecover(t *testing.T) {
	m := testManager(t)
	p, err := m.Start("sleep", "sleep 30")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.Stop(p.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	for e := range m.Events() {
		if e.Kind == "recover" {
			t.Fatalf("unexpected recover event after user stop: %+v", e)
		}
		if e.Kind == "exit" {
			break
		}
	}
}

func TestAttemptsCap(t *testing.T) {
	settings := config.Settings{Resilience: &config.ResilienceSettings{MaxProcessRecoveries: 2}}
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)
	m := NewManager(dir, run.Config{}, nil, settings, posture.Defaults(), tools.Capabilities{})

	if _, err := m.Start("false", "false"); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The first exit dispatches recovery; the recovery subagent cannot run
	// without a client, so the process just sits in StateRecovering. Burn the
	// remaining attempts by restarting with the same command, which counts as
	// one attempt and exits immediately again.
	time.Sleep(200 * time.Millisecond)
	p, ok := m.Lookup("p0")
	if !ok || p.State != StateRecovering {
		t.Fatalf("expected recovering, got %+v", p)
	}
	_ = m.RestartProcess(p.ID, "false")
	time.Sleep(200 * time.Millisecond)
	_ = m.RestartProcess(p.ID, "false")
	time.Sleep(200 * time.Millisecond)

	p, ok = m.Lookup("p0")
	if ok {
		t.Fatalf("expected process removed after cap, got %+v", p)
	}
	sawFail := false
	drainEvents(m, func(e Event) bool {
		if e.Kind == "fail" {
			sawFail = true
		}
		return sawFail
	})
	if !sawFail {
		t.Fatal("expected fail event after attempt cap")
	}
}

func TestShutdownKillsGroup(t *testing.T) {
	m := testManager(t)
	// A child that ignores SIGTERM and would outlive a plain Kill on the
	// parent. The process group kill should take it down.
	script := "sleep 30 & sleep 30 & wait"
	p, err := m.Start("sleeper", script)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	m.Shutdown()
	time.Sleep(500 * time.Millisecond)

	// The script shell and its children should be gone.
	procs, _ := os.FindProcess(p.PID)
	if procs != nil && procs.Signal(os.Signal(nil)) == nil {
		t.Fatalf("process group %d still alive", p.PID)
	}
}

func TestLockReclaimsStalePID(t *testing.T) {
	m := testManager(t)
	// Write a lock file claiming a pid that cannot possibly be alive.
	lockPath := filepath.Join(m.logsDir, lockName(m.workdir, "sleep"))
	if err := os.WriteFile(lockPath, []byte("9999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("sleep", "sleep 30"); err != nil {
		t.Fatalf("stale lock reclaim failed: %v", err)
	}
}

func TestLockRemovedAfterStop(t *testing.T) {
	m1 := testManager(t)
	dir := m1.workdir
	p, err := m1.Start("sleep", "sleep 30")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	m1.Stop(p.ID)
	time.Sleep(200 * time.Millisecond)

	m2 := NewManager(dir, run.Config{}, nil, config.Settings{}, posture.Defaults(), tools.Capabilities{})
	if _, err := m2.Start("sleep", "sleep 30"); err != nil {
		t.Fatalf("start after stop failed: %v", err)
	}
}

func TestLogGrepLineNumbersAndContext(t *testing.T) {
	m := testManager(t)
	entry, _ := m.Start("echo", "echo one && echo two && echo MATCH && echo four")
	// The command exits quickly; logs are still open.
	time.Sleep(200 * time.Millisecond)
	out, err := m.LogGrep(entry.ID, "MATCH", 10, 1)
	if err != nil {
		t.Fatalf("LogGrep: %v", err)
	}
	if !strings.Contains(out, fmt.Sprintf("%s:3: MATCH", entry.ID)) {
		t.Fatalf("expected MATCH line, got %q", out)
	}
	if !strings.Contains(out, "two") || !strings.Contains(out, "four") {
		t.Fatalf("expected context lines, got %q", out)
	}
	if strings.Contains(out, "one") {
		t.Fatalf("did not expect context beyond one line, got %q", out)
	}
}

func TestTailAfterExit(t *testing.T) {
	m := testManager(t)
	p, err := m.Start("echo", "echo alpha && echo beta")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// A process that exits unexpectedly stays in the active map in the
	// recovering state; Tail should still resolve it by name.
	tail, err := m.Tail("echo", 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if !strings.Contains(tail, "alpha") || !strings.Contains(tail, "beta") {
		t.Fatalf("tail = %q, want alpha and beta", tail)
	}
	_ = p
}

func TestHistorySurvivesStop(t *testing.T) {
	m := testManager(t)
	p, err := m.Start("echo", "echo alpha && sleep 60")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := m.Stop(p.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Stopped processes are removed from the active map, but their snapshot
	// and log path are preserved in history under the process name.
	if _, ok := m.Lookup(p.ID); ok {
		t.Fatal("stopped process should not be in active map")
	}
	hist, ok := m.ProcessByName("echo")
	if !ok {
		t.Fatal("expected history entry for echo")
	}
	if hist.LogPath == "" {
		t.Fatal("history must retain the log path")
	}

	tail, err := m.Tail("echo", 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if !strings.Contains(tail, "alpha") {
		t.Fatalf("tail = %q, want alpha", tail)
	}
}

func TestTailByIDAndLogDirectory(t *testing.T) {
	m := testManager(t)
	// The default log directory should sit under SIGNET_HOME/signet/logs.
	if !strings.Contains(m.logsDir, string(filepath.Separator)+"logs") {
		t.Fatalf("logsDir = %q, want .../logs", m.logsDir)
	}
	p, err := m.Start("echo", "echo hello log")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	tail, err := m.TailByID(p.ID, 10)
	if err != nil {
		t.Fatalf("TailByID: %v", err)
	}
	if !strings.Contains(tail, "hello log") {
		t.Fatalf("tail = %q, want hello log", tail)
	}
}

func drainEvents(m *Manager, stop func(Event) bool) {
	timeout := time.After(2 * time.Second)
	for {
		select {
		case e := <-m.Events():
			if stop(e) {
				return
			}
		case <-timeout:
			return
		}
	}
}
