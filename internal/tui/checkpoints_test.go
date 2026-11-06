package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/checkpoint"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sessionctl"
)

// ckptApp is treeApp (u1 a1 u2 a2 in session "sess-1") with a file the second
// prompt changed: its checkpoint names entry u2.
func ckptApp(t *testing.T) (*App, string) {
	t.Helper()
	a, _ := treeApp(t)
	root, err := filepath.EvalSymlinks(a.workdir)
	if err != nil {
		t.Fatal(err)
	}
	a.workdir = root
	file := filepath.Join(root, "f.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.turnUserEntry = "u2"
	a.beginCheckpointTurn("second")
	tr := a.checkpointTracker()
	if tr == nil {
		t.Fatal("no tracker")
	}
	if err := tr.Save(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return a, file
}

func ckptKey(a *App, s string) { a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }

func TestUndoRestoresOnlyAfterConfirmation(t *testing.T) {
	a, file := ckptApp(t)
	if cmd := a.handleCommand("/undo"); cmd != nil {
		t.Logf("returned a command")
	}
	if !a.viewOpen(viewRewind) {
		t.Fatalf("no confirmation opened: %q", lastSystem(a))
	}
	if b, _ := os.ReadFile(file); string(b) != "after\n" {
		t.Fatal("the file changed before the user confirmed")
	}
	v := a.View()
	if !strings.Contains(v, "f.txt") || !strings.Contains(v, "restore") || !strings.Contains(v, "second") {
		t.Fatalf("confirmation does not list the file and the prompt:\n%s", v)
	}
	ckptKey(a, "n")
	if a.viewOpen(viewRewind) {
		t.Fatal("n did not close the view")
	}
	if b, _ := os.ReadFile(file); string(b) != "after\n" {
		t.Fatal("cancelling restored the file")
	}

	a.handleCommand("/undo")
	ckptKey(a, "y")
	if b, _ := os.ReadFile(file); string(b) != "before\n" {
		t.Fatalf("file = %q after confirming", b)
	}
	if a.viewOpen(viewRewind) || !strings.Contains(lastSystem(a), "restored 1") || !strings.Contains(lastSystem(a), "safety") && !strings.Contains(lastSystem(a), "kept as checkpoint") {
		t.Fatalf("view open = %v, last = %q", a.viewOpen(viewRewind), lastSystem(a))
	}
	// A second /undo has nothing left to undo.
	a.handleCommand("/undo")
	if a.viewOpen(viewRewind) || !strings.Contains(lastSystem(a), "nothing to undo") {
		t.Fatalf("second undo: %q", lastSystem(a))
	}
}

func TestUndoAndRewindAreRefusedWhileATurnRuns(t *testing.T) {
	for _, cmd := range []string{"/undo", "/rewind"} {
		a, file := ckptApp(t)
		a.cancel = func() {}
		a.handleCommand(cmd)
		if a.viewOpen(viewRewind) || !strings.Contains(lastSystem(a), "running turn") {
			t.Errorf("%s mid-turn: open=%v msg=%q", cmd, a.viewOpen(viewRewind), lastSystem(a))
		}
		if b, _ := os.ReadFile(file); string(b) != "after\n" {
			t.Errorf("%s mid-turn changed a file", cmd)
		}
	}
}

func TestApplyIsRefusedIfATurnStartsWhileConfirming(t *testing.T) {
	a, file := ckptApp(t)
	a.handleCommand("/undo")
	a.cancel = func() {}
	ckptKey(a, "y")
	if b, _ := os.ReadFile(file); string(b) != "after\n" {
		t.Fatal("a restore ran under a running turn")
	}
	if !a.viewOpen(viewRewind) || !strings.Contains(a.rewind.err, "running turn") {
		t.Fatalf("err = %q", a.rewind.err)
	}
}

func TestUndoAndRewindNeedCheckpoints(t *testing.T) {
	a, _ := ckptApp(t)
	no := false
	a.settings.Checkpoints = &config.CheckpointSettings{Enabled: &no}
	for _, cmd := range []string{"/undo", "/rewind"} {
		a.handleCommand(cmd)
		if a.viewOpen(viewRewind) || !strings.Contains(lastSystem(a), "checkpoints are off") {
			t.Errorf("%s: open=%v msg=%q", cmd, a.viewOpen(viewRewind), lastSystem(a))
		}
	}
	if a.checkpointTracker() != nil {
		t.Fatal("a disabled session still has a tracker")
	}

	b, _ := treeApp(t)
	b.handleCommand("/undo")
	if !strings.Contains(lastSystem(b), "nothing to undo") {
		t.Fatalf("no checkpoints yet: %q", lastSystem(b))
	}
	b.handleCommand("/rewind")
	if b.viewOpen(viewRewind) || !strings.Contains(lastSystem(b), "no checkpoints yet") {
		t.Fatalf("/rewind with none: %q", lastSystem(b))
	}
}

func TestRewindBothRestoresFilesAndBranchesTheConversation(t *testing.T) {
	a, file := ckptApp(t)
	a.handleCommand("/rewind")
	if !a.viewOpen(viewRewind) || !strings.Contains(a.View(), "second") {
		t.Fatalf("the picker does not list the checkpoint:\n%s", a.View())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick it
	if a.rewind.stage != rewindChoose {
		t.Fatalf("stage = %v", a.rewind.stage)
	}
	for i := 0; i < 2; i++ { // code, conversation, both
		a.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.rewind.stage != rewindConfirm || a.rewind.mode != rewindBoth {
		t.Fatalf("stage = %v mode = %v err = %q", a.rewind.stage, a.rewind.mode, a.rewind.err)
	}
	if b, _ := os.ReadFile(file); string(b) != "after\n" {
		t.Fatal("the file changed before the user confirmed")
	}
	ckptKey(a, "y")
	if b, _ := os.ReadFile(file); string(b) != "before\n" {
		t.Fatalf("file = %q", b)
	}
	if got := chatTexts(a); got != "first,one" {
		t.Fatalf("conversation = %q, want it back before the second prompt", got)
	}
	if a.editor.Value() != "second" {
		t.Fatalf("the prompt did not go back into the editor: %q", a.editor.Value())
	}
	if a.sessionID != "sess-1" {
		t.Fatalf("session changed to %q", a.sessionID)
	}
}

func TestRewindConversationOnlyLeavesFilesAlone(t *testing.T) {
	a, file := ckptApp(t)
	a.handleCommand("/rewind")
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.rewind.mode != rewindConversation || a.rewind.stage != rewindConfirm {
		t.Fatalf("mode = %v stage = %v", a.rewind.mode, a.rewind.stage)
	}
	ckptKey(a, "y")
	if b, _ := os.ReadFile(file); string(b) != "after\n" {
		t.Fatalf("a conversation rewind touched the file: %q", b)
	}
	if got := chatTexts(a); got != "first,one" {
		t.Fatalf("conversation = %q", got)
	}
}

func TestRewindOffersNoConversationForTheFirstPrompt(t *testing.T) {
	a, _ := ckptApp(t)
	// A checkpoint whose prompt is the first entry has nothing before it.
	a.turnUserEntry = "u1"
	a.beginCheckpointTurn("first")
	root := a.workdir
	other := filepath.Join(root, "g.txt")
	_ = os.WriteFile(other, []byte("g"), 0o644)
	_ = a.checkpointTracker().Save(other)
	a.handleCommand("/rewind")
	a.Update(tea.KeyMsg{Type: tea.KeyEnter}) // newest checkpoint, the u1 one
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a.Update(tea.KeyMsg{Type: tea.KeyEnter}) // conversation
	if a.rewind.stage != rewindChoose || !strings.Contains(a.rewind.err, "no earlier message") {
		t.Fatalf("stage = %v err = %q", a.rewind.stage, a.rewind.err)
	}
}

func TestEscClosesTheRewindViewWithoutRestoring(t *testing.T) {
	a, file := ckptApp(t)
	a.handleCommand("/rewind")
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a.Update(tea.KeyMsg{Type: tea.KeyEnter}) // code -> confirm
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})   // back to the choice
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})   // back to the list
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})   // close
	if a.viewOpen(viewRewind) {
		t.Fatal("esc did not close the view")
	}
	if b, _ := os.ReadFile(file); string(b) != "after\n" {
		t.Fatal("closing restored the file")
	}
}

func TestConfirmationShowsOnlyCleanedText(t *testing.T) {
	a, _ := ckptApp(t)
	a.turnUserEntry = "u2"
	a.beginCheckpointTurn("line\x1b[31m red\nsecond line \u202e")
	plain := filepath.Join(a.workdir, "plain.txt")
	if err := a.checkpointTracker().Save(plain); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.handleCommand("/undo")
	v := a.View()
	if strings.Contains(v, "\x1b[31m") || strings.Contains(v, "\u202e") {
		t.Fatalf("control or bidi text reached the screen:\n%q", v)
	}
}

func TestBeginCheckpointTurnInvalidatesAnAgentBuiltWithoutTheTracker(t *testing.T) {
	a, _ := treeApp(t)
	a.agentCkpt = nil
	a.beginCheckpointTurn("p")
	if a.agentCkpt == nil || a.agentCkpt != a.ckpt {
		t.Fatal("the tracker was not handed to the next agent session")
	}
	if a.turnUserEntry != "" {
		t.Fatal("the prompt entry marker was not consumed")
	}
	// A new session id gets a new store.
	old := a.ckpt
	a.sessionID = "sess-2"
	a.beginCheckpointTurn("q")
	if a.ckpt == old || !strings.Contains(a.ckpt.Dir(), "sess-2") {
		t.Fatalf("store dir = %q", a.ckpt.Dir())
	}
}

func TestAnUnopenableStoreFailsClosedWithANotice(t *testing.T) {
	a, _ := treeApp(t)
	// The state directory is a file, so no store can be created in it.
	home := t.TempDir()
	blocker := filepath.Join(home, "belai")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BELAI_HOME", blocker)
	a.beginCheckpointTurn("p")
	tr := a.ckpt
	if tr == nil {
		t.Fatal("no tracker: the session would run unprotected")
	}
	if err := tr.Ready(); err == nil {
		t.Fatal("an unopenable store reported ready")
	}
	if !strings.Contains(lastSystem(a), "checkpoints:") {
		t.Fatalf("no notice: %q", lastSystem(a))
	}
}

func TestSessionControlsDoNotIncludeUndoOrRewind(t *testing.T) {
	for _, line := range []string{"/undo", "/rewind"} {
		if sessionctl.IsCommand(line) {
			t.Errorf("%s is a web session control", line)
		}
	}
}

func TestRewindPolicyConfinesRestoresToTheSessionRoots(t *testing.T) {
	a, _ := ckptApp(t)
	outside := filepath.Join(t.TempDir(), "o.txt")
	_ = os.WriteFile(outside, []byte("orig"), 0o644)
	tr := a.checkpointTracker()
	a.turnUserEntry = ""
	a.beginCheckpointTurn("outside")
	if err := tr.Save(outside); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(outside, []byte("changed"), 0o644)
	info, _ := tr.Latest()
	plan, err := tr.Plan(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Changes {
		if c.Path == outside && c.Action != checkpoint.ActSkip {
			t.Fatalf("a path outside every root would be written: %+v", c)
		}
	}
}
