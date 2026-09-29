package bgproc

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/tools"
)

func waitState(t *testing.T, m *Manager, id, want string) tools.BackgroundInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, i := range m.ListBackground() {
			if i.ID == id && i.State == want {
				return i
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %s never reached %q: %+v", id, want, m.ListBackground())
	return tools.BackgroundInfo{}
}

func TestBackgroundReadsOnlyNewOutput(t *testing.T) {
	m := testManager(t)
	info, err := m.StartBackground("echo one; sleep 0.3; echo two", "", sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, info.ID, "exited")
	out, got, err := m.ReadBackground(info.ID, "", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "one") || !strings.Contains(out, "two") {
		t.Fatalf("first read = %q", out)
	}
	if got.State != "exited" {
		t.Fatalf("state = %q", got.State)
	}
	out, _, _ = m.ReadBackground(info.ID, "", 1024)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("second read should be empty, got %q", out)
	}
}

func TestBackgroundFilterSkipsLinesForGood(t *testing.T) {
	m := testManager(t)
	info, _ := m.StartBackground("printf 'a\\nERR b\\nc\\n'", "", sandbox.Policy{})
	waitState(t, m, info.ID, "exited")
	out, _, _ := m.ReadBackground(info.ID, "ERR", 1024)
	if strings.TrimSpace(out) != "ERR b" {
		t.Fatalf("filtered = %q", out)
	}
	out, _, _ = m.ReadBackground(info.ID, "", 1024)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("skipped lines must not come back: %q", out)
	}
}

func TestBackgroundReadCapResumesOnLineBoundary(t *testing.T) {
	m := testManager(t)
	info, _ := m.StartBackground("printf 'aaaa\\nbbbb\\ncccc\\n'", "", sandbox.Policy{})
	waitState(t, m, info.ID, "exited")
	out, _, _ := m.ReadBackground(info.ID, "", 7)
	if !strings.HasPrefix(out, "aaaa\n") || !strings.Contains(out, "more output is waiting") {
		t.Fatalf("capped read = %q", out)
	}
	out, _, _ = m.ReadBackground(info.ID, "", 1024)
	if !strings.HasPrefix(out, "bbbb\ncccc") {
		t.Fatalf("resumed read = %q", out)
	}
}

func TestBackgroundExitIsNeverRecovered(t *testing.T) {
	m := testManager(t)
	info, _ := m.StartBackground("exit 3", "", sandbox.Policy{})
	got := waitState(t, m, info.ID, "exited")
	if got.ExitCode != 3 {
		t.Fatalf("exit code = %d", got.ExitCode)
	}
	time.Sleep(200 * time.Millisecond)
	drainEvents(m, func(e Event) bool {
		if e.Kind == "recover" || e.Kind == "fail" {
			t.Fatalf("model-started process must not be recovered: %s", e.Kind)
		}
		return false
	})
}

func TestBackgroundStopKeepsOutputReadable(t *testing.T) {
	m := testManager(t)
	info, _ := m.StartBackground("echo up; sleep 30", "", sandbox.Policy{})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _, _ := m.ReadBackground(info.ID, "", 1024)
		if strings.Contains(out, "up") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := m.StopBackground(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "stopped" {
		t.Fatalf("state = %q", got.State)
	}
	if _, _, err := m.ReadBackground(info.ID, "", 10); err != nil {
		t.Fatalf("stopped process must stay readable: %v", err)
	}
	if _, err := m.StopBackground(info.ID); err != nil {
		t.Fatalf("second stop is a no-op: %v", err)
	}
}

func TestBackgroundToolsCannotReachUserProcesses(t *testing.T) {
	m := testManager(t)
	p, err := m.Start("sleep", "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop(p.ID)
	if _, _, err := m.ReadBackground(p.ID, "", 10); err == nil {
		t.Fatal("ReadBackground reached a user process")
	}
	if _, err := m.StopBackground(p.ID); err == nil {
		t.Fatal("StopBackground stopped a user process")
	}
	if len(m.ListBackground()) != 0 {
		t.Fatal("a user process is listed as background")
	}
}

func TestBackgroundProcessIsNotRestartable(t *testing.T) {
	m := testManager(t)
	info, _ := m.StartBackground("sleep 30", "", sandbox.Policy{})
	defer m.StopBackground(info.ID)
	if _, ok := m.ProcessCommand(info.ID); ok {
		t.Fatal("ProcessCommand exposes a background process")
	}
	if err := m.RestartProcess(info.ID, "sleep 30"); err == nil {
		t.Fatal("RestartProcess restarted a background process")
	}
}

func TestBackgroundCap(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BELAI_HOME", t.TempDir())
	set := config.Settings{Resilience: &config.ResilienceSettings{MaxBackgroundProcesses: 2}}
	m := NewManager(dir, run.Config{}, nil, set, posture.Defaults(), tools.Capabilities{})
	defer m.Shutdown()
	a, err := m.StartBackground("sleep 30", "", sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartBackground("sleep 30", "", sandbox.Policy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartBackground("sleep 30", "", sandbox.Policy{}); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("third start = %v, want the cap error", err)
	}
	if _, err := m.StopBackground(a.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, a.ID, "stopped")
	if _, err := m.StartBackground("sleep 30", "", sandbox.Policy{}); err != nil {
		t.Fatalf("a stopped process frees a slot: %v", err)
	}
}

func TestBackgroundEmptyCommand(t *testing.T) {
	m := testManager(t)
	if _, err := m.StartBackground("  ", "", sandbox.Policy{}); err == nil {
		t.Fatal("empty command accepted")
	}
}

func TestBackgroundSameBinaryTwice(t *testing.T) {
	m := testManager(t)
	defer m.Shutdown()
	if _, err := m.StartBackground("sleep 30", "", sandbox.Policy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartBackground("sleep 30", "", sandbox.Policy{}); err != nil {
		t.Fatalf("two launches of one binary must not share a lock: %v", err)
	}
}
