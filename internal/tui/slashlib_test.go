package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/bgproc"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/processlib"
	"github.com/vulnetix/belai/internal/promptlib"
)

// slashLibApp builds an App with one saved prompt, one agent profile and one
// saved process, and stops any process a test starts.
func slashLibApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if _, err := promptlib.Create(config.ScopeProject, workdir, "deploy", "ship it to staging"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	proc, err := processlib.CreateUnique(config.ScopeProject, workdir, "sleep 30")
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	// Disabled so New does not auto-start it; the popup still offers it.
	if _, err := processlib.SetEnabled(proc, false); err != nil {
		t.Fatalf("disable process: %v", err)
	}
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: workdir})
	a.loadAgents()
	t.Cleanup(func() {
		if a.procManager != nil {
			a.procManager.Shutdown()
		}
	})
	return a
}

func typeSlash(a *App, line string) {
	a.editor.SetValue(line)
	a.editor.CursorEnd()
	a.refreshAutocomplete()
}

func TestSlashPopupListsLibraries(t *testing.T) {
	a := slashLibApp(t)
	typeSlash(a, "/")
	for _, want := range []string{"/prompts", "/prompt:deploy", "/agent:reviewer", "/process:sleep"} {
		if !slices.Contains(a.autocomplete, want) {
			t.Fatalf("popup for / = %v, missing %s", a.autocomplete, want)
		}
	}
	// Commands keep their place ahead of the libraries on an empty query.
	if i, j := slices.Index(a.autocomplete, "/yolo"), slices.Index(a.autocomplete, "/prompt:deploy"); i > j {
		t.Fatalf("commands should precede library entries: %v", a.autocomplete)
	}
}

func TestSlashPopupFuzzyMatchesLibraries(t *testing.T) {
	a := slashLibApp(t)
	cases := map[string]string{
		"/dpl":      "/prompt:deploy",
		"/deploy":   "/prompt:deploy",
		"/a:rev":    "/agent:reviewer",
		"/process:": "/process:sleep",
	}
	for line, want := range cases {
		typeSlash(a, line)
		if !slices.Contains(a.autocomplete, want) {
			t.Errorf("popup for %s = %v, missing %s", line, a.autocomplete, want)
		}
	}
	typeSlash(a, "/deploy")
	if a.autocomplete[0] != "/prompt:deploy" {
		t.Fatalf("an exact name should rank first: %v", a.autocomplete)
	}
	typeSlash(a, "/prompt:deploy extra")
	if len(a.autocomplete) != 0 {
		t.Fatalf("a space ends name completion, got %v", a.autocomplete)
	}
}

func TestSlashPromptChipPrefillsComposer(t *testing.T) {
	a := slashLibApp(t)
	typeSlash(a, "/prompt:dep")
	a.autocompleteIndex = slices.Index(a.autocomplete, "/prompt:deploy")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if got := a.editor.Value(); got != "ship it to staging" {
		t.Fatalf("composer = %q, want the prompt body", got)
	}
	if a.loadedPrompt == nil || a.loadedPrompt.Name != "deploy" {
		t.Fatalf("loadedPrompt = %+v, want deploy", a.loadedPrompt)
	}
	for _, m := range a.messages {
		if m.Role == "user" {
			t.Fatalf("selecting a prompt must not send a turn")
		}
	}
}

func TestSlashAgentEngagesProfileFromPlanMode(t *testing.T) {
	a := slashLibApp(t)
	a.mode = "plan"
	a.syncPlanMode()
	typeSlash(a, "/agent:reviewer")
	// Typed in full and entered, with nothing highlighted.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.mode != "agent" || a.planMode {
		t.Fatalf("mode = %q planMode = %v, want agent", a.mode, a.planMode)
	}
	if a.namedAgent != "reviewer" {
		t.Fatalf("namedAgent = %q, want reviewer", a.namedAgent)
	}

	a.engageAgent("nobody")
	if a.namedAgent != "reviewer" {
		t.Fatalf("an unknown name must not change the agent")
	}
}

func TestSlashProcessStartsOnceAndReportsStatus(t *testing.T) {
	a := slashLibApp(t)
	if cmd := a.runProcessEntry("sleep"); cmd == nil {
		t.Fatalf("expected the process to start")
	}
	p, ok := a.procManager.ProcessByName("sleep")
	if !ok || p.State != bgproc.StateRunning {
		t.Fatalf("process = %+v ok=%v, want running", p, ok)
	}
	if last := a.messages[len(a.messages)-1].Content; !strings.Contains(last, "process sleep") || !strings.Contains(last, "running") {
		t.Fatalf("status line = %q", last)
	}

	if cmd := a.runProcessEntry("sleep"); cmd != nil {
		t.Fatalf("a running process must not be started again")
	}
	running := 0
	for _, q := range a.procManager.List() {
		if q.Name == "sleep" && q.State == bgproc.StateRunning {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("running copies = %d, want 1", running)
	}
	if last := a.messages[len(a.messages)-1].Content; !strings.Contains(last, "running") {
		t.Fatalf("second call should still report status, got %q", last)
	}
}

func TestProcessStatusLine(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	live := bgproc.Process{ID: "p1", Name: "web", Command: "npm run dev", State: bgproc.StateRunning, PID: 42, Started: now.Add(-90 * time.Second), LogPath: "/logs/web.log"}
	want := "process web (p1) running · pid 42 · up 1m30s · npm run dev · log /logs/web.log"
	if got := processStatusLine(live, now); got != want {
		t.Fatalf("live = %q, want %q", got, want)
	}
	done := bgproc.Process{ID: "p2", Name: "web", State: bgproc.StateExited, ExitCode: 1, Ended: now.Add(-5 * time.Second)}
	if got := processStatusLine(done, now); got != "process web (p2) exited · exit 1 · ended 5s ago" {
		t.Fatalf("exited = %q", got)
	}
}

func TestChipWindowKeepsSelectionVisible(t *testing.T) {
	parts := []string{"aaaa", "bbbb", "cccc", "dddd", "eeee"}
	// Room for two chips plus separators and both markers.
	lo, hi := chipWindow(parts, 3, 4+2+4+2+2, 2)
	if lo > 3 || hi <= 3 {
		t.Fatalf("window [%d,%d) hides selection 3", lo, hi)
	}
	if lo, hi := chipWindow(parts, 0, 1000, 2); lo != 0 || hi != len(parts) {
		t.Fatalf("wide window = [%d,%d), want everything", lo, hi)
	}
}

func TestIsLibraryLine(t *testing.T) {
	for line, want := range map[string]bool{
		"/prompt:x":        true,
		"/agent:belai:dbg": true,
		"/process:web":     true,
		"/prompts":         false,
		"/agent start x":   false,
		"prompt:x":         true,
	} {
		if got := isLibraryLine(line); got != want {
			t.Errorf("isLibraryLine(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestLibraryCommandUnknownNamesChangeNothing(t *testing.T) {
	a := slashLibApp(t)
	for _, line := range []string{"/prompt:nope", "/agent:nope", "/process:nope"} {
		before := a.editor.Value()
		if _, ok := a.handleLibraryCommand(line); !ok {
			t.Fatalf("%s should be claimed as a library line", line)
		}
		last := a.messages[len(a.messages)-1].Content
		if !strings.Contains(last, "no ") || !strings.Contains(last, "nope") {
			t.Fatalf("%s: refusal = %q", line, last)
		}
		if a.editor.Value() != before {
			t.Fatalf("%s changed the composer", line)
		}
	}
	if _, ok := a.handleLibraryCommand("/prompts"); ok {
		t.Fatalf("/prompts is a command, not a library line")
	}
}

func TestAgentNamesWithColonKeepTheirSuffix(t *testing.T) {
	a := slashLibApp(t)
	a.agents = append(a.agents, agentChoice{Name: "team:lead"})
	if _, ok := a.handleLibraryCommand("/agent:team:lead"); !ok || a.namedAgent != "team:lead" {
		t.Fatalf("namedAgent = %q, want team:lead", a.namedAgent)
	}
}

func TestProcessBlockedInPlanMode(t *testing.T) {
	a := slashLibApp(t)
	a.mode = "plan"
	if cmd := a.runProcessEntry("sleep"); cmd != nil {
		t.Fatalf("plan mode must refuse to start a process")
	}
	if _, ok := a.procManager.ProcessByName("sleep"); ok {
		t.Fatalf("nothing should have started")
	}
}
