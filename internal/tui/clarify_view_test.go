package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/clarify"
)

func sampleQuestionnaire() clarify.Questionnaire {
	return clarify.Questionnaire{Groups: []clarify.Group{
		{Context: "Which pool?", Options: []clarify.Option{
			{Label: "Parent", Description: "reuse session pool"},
			{Label: "Child", Description: "local pool per round"},
		}},
		{Context: "Which tools?", Multi: true, Options: []clarify.Option{
			{Label: "Read"},
			{Label: "Bash"},
			{Label: "Grep"},
		}},
	}}
}

func TestEventClarifyAskPushesViewAndStopsPump(t *testing.T) {
	a := New(Options{})
	a.cancel = func() {}
	q := sampleQuestionnaire()
	reply := make(chan clarify.Answers)

	_, cmd := a.Update(agentEventMsg{Kind: agent.EventClarifyAskKind, Clarify: &q, Reply: reply})

	if a.view != viewClarify {
		t.Fatalf("expected viewClarify, got %d", a.view)
	}
	if a.clarifyState.reply != reply {
		t.Fatalf("clarify state did not capture reply channel")
	}
	if cmd != nil {
		t.Fatalf("expected no command; the agent pump must not re-arm yet")
	}
}

func TestClarifyCursorSkipsHeaders(t *testing.T) {
	a := New(Options{})
	a.push(viewClarify)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), make(chan clarify.Answers))

	// initial selection should be on the first option row (index 1 because
	// index 0 is the first header).
	if a.clarifyState.selected != 1 {
		t.Fatalf("initial selected = %d, want 1", a.clarifyState.selected)
	}

	// Move up from the first option should stay at the first option, not land
	// on the header.
	m, _ := a.Update(keyMsg("up"))
	a = m.(*App)
	if a.clarifyState.selected != 1 {
		t.Fatalf("up moved onto header: selected = %d", a.clarifyState.selected)
	}

	// Move down through the second group. There are 2 options in group 1,
	// so from index 1, down -> index 2 (option), down -> index 4 (group 2
	// option), skipping the header at index 3.
	m, _ = a.Update(keyMsg("down"))
	a = m.(*App)
	if a.clarifyState.selected != 2 {
		t.Fatalf("down selected = %d, want 2", a.clarifyState.selected)
	}
	m, _ = a.Update(keyMsg("down"))
	a = m.(*App)
	if a.clarifyState.selected != 4 {
		t.Fatalf("down over header selected = %d, want 4", a.clarifyState.selected)
	}
}

func TestClarifySpaceRadioForSingle(t *testing.T) {
	a := New(Options{})
	a.push(viewClarify)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), make(chan clarify.Answers))

	// group 0 is single-select; choosing child should replace parent.
	m, _ := a.Update(keyMsg("down")) // move to Child
	a = m.(*App)
	m, _ = a.Update(keyMsg(" "))
	a = m.(*App)
	if a.clarifyState.chosen[0][1] != true {
		t.Fatalf("expected Child selected")
	}
	if a.clarifyState.chosen[0][0] {
		t.Fatalf("single-select should not keep Parent")
	}
}

func TestClarifySpaceCheckboxForMulti(t *testing.T) {
	a := New(Options{})
	a.push(viewClarify)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), make(chan clarify.Answers))

	// Move to group 2 (multi) option Read at row index 4.
	m, _ := a.Update(keyMsg("down"))
	a = m.(*App)
	m, _ = a.Update(keyMsg("down"))
	a = m.(*App)
	m, _ = a.Update(keyMsg(" "))
	a = m.(*App)
	m, _ = a.Update(keyMsg("down"))
	a = m.(*App)
	m, _ = a.Update(keyMsg(" "))
	a = m.(*App)

	if !a.clarifyState.chosen[1][0] {
		t.Fatalf("expected Read selected")
	}
	if !a.clarifyState.chosen[1][1] {
		t.Fatalf("expected Bash selected")
	}
	if a.clarifyState.chosen[1][2] {
		t.Fatalf("did not expect Grep selected")
	}
}

func TestClarifyNoteOpensAndCommits(t *testing.T) {
	a := New(Options{})
	a.push(viewClarify)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), make(chan clarify.Answers))

	m, _ := a.Update(keyMsg("n"))
	a = m.(*App)
	if !a.clarifyState.noteMode {
		t.Fatalf("expected note mode")
	}

	a.editor.SetValue("use seed 42")
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)

	if a.clarifyState.noteMode {
		t.Fatalf("note mode should exit after commit")
	}
	if got := a.clarifyState.notes[[2]int{0, 0}]; got != "use seed 42" {
		t.Fatalf("expected note stored, got %q", got)
	}
}

func TestClarifySkipMarksGroupSkipped(t *testing.T) {
	a := New(Options{})
	a.push(viewClarify)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), make(chan clarify.Answers))

	// Select an option in group 0 first, then skip the group.
	m, _ := a.Update(keyMsg(" "))
	a = m.(*App)
	if !a.clarifyState.chosen[0][0] {
		t.Fatalf("expected an option chosen before skip")
	}
	m, _ = a.Update(keyMsg("s"))
	a = m.(*App)
	if !a.clarifyState.skipped[0] {
		t.Fatalf("expected group 0 skipped")
	}
	if len(a.clarifyState.chosen[0]) != 0 {
		t.Fatalf("skipped group should clear choices")
	}
}

func TestClarifyEnterSendsAnswersAndRearmsPump(t *testing.T) {
	a := New(Options{})
	a.push(viewClarify)
	reply := make(chan clarify.Answers)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), reply)

	// Choose Parent in group 0.
	m, _ := a.Update(keyMsg(" "))
	a = m.(*App)

	// Arm a closed event channel so the re-armed pump command is drainable.
	ch := make(chan agent.Event)
	a.events = ch
	close(ch)

	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.view != viewChat {
		t.Fatalf("expected pop back to chat, got %d", a.view)
	}

	select {
	case ans := <-reply:
		if len(ans.Items) != 2 {
			t.Fatalf("expected 2 answers, got %d", len(ans.Items))
		}
		if ans.Items[0].Skipped || len(ans.Items[0].Chosen) != 1 || ans.Items[0].Chosen[0] != 0 {
			t.Fatalf("expected Parent chosen, got %+v", ans.Items[0])
		}
	case <-time.After(time.Second):
		t.Fatalf("no answer sent on reply channel")
	}

	if cmd == nil {
		t.Fatalf("expected pump to re-arm")
	}
	// The returned command should produce the next agent event.
	msg := cmd()
	if _, ok := msg.(agentEventMsg); !ok {
		t.Fatalf("expected agentEventMsg, got %T", msg)
	}
}

// Enter on an option picks it when its question has no answer yet. It used
// to submit without selecting, so a user who moved to a file and pressed
// enter sent "chose: (none)" and was asked the same question again. The
// answers are also recorded in the transcript beside the questionnaire.
func TestClarifyEnterChoosesHighlightedOption(t *testing.T) {
	a := New(Options{})
	a.push(viewClarify)
	reply := make(chan clarify.Answers, 1)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), reply)

	m, _ := a.Update(keyMsg("down")) // Parent → Child
	a = m.(*App)
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case ans := <-reply:
		if len(ans.Items[0].Chosen) != 1 || ans.Items[0].Chosen[0] != 1 {
			t.Fatalf("enter on Child must choose it, got %+v", ans.Items[0])
		}
		if len(ans.Items[1].Chosen) != 0 {
			t.Fatalf("an untouched question must stay unanswered, got %+v", ans.Items[1])
		}
	case <-time.After(time.Second):
		t.Fatal("no answer sent on reply channel")
	}
	last := a.messages[len(a.messages)-1].Text()
	if !strings.Contains(last, "chose: Child") {
		t.Fatalf("answers not recorded in the transcript: %q", last)
	}
}

func TestClarifyEscCancelsTurn(t *testing.T) {
	a := New(Options{})
	a.push(viewClarify)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), make(chan clarify.Answers))

	cancelled := false
	a.cancel = func() { cancelled = true }
	a.events = make(chan agent.Event) // any non-nil channel to check clearing

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(*App)

	if a.view != viewChat {
		t.Fatalf("expected pop back to chat, got %d", a.view)
	}
	if !cancelled {
		t.Fatalf("expected context cancelled")
	}
	if a.cancel != nil {
		t.Fatalf("expected cancel cleared")
	}
	if a.events != nil {
		t.Fatalf("expected events cleared")
	}
}

func keyMsg(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// TestClarifyReplacesComposerAndFooter checks that the clarify questionnaire
// is rendered as a bottom panel over the chat transcript, sized to its
// content, and that the composer and footer are hidden while it is active.
func TestClarifyReplacesComposerAndFooter(t *testing.T) {
	a := New(Options{})
	a.width = 120
	a.height = 40
	a.cfg.Model = "test-clarify-model"

	reply := make(chan clarify.Answers)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), reply)
	a.view = viewClarify

	view := a.chatView()

	if !strings.Contains(view, "Clarify") {
		t.Fatalf("chatView should render the clarify panel header")
	}
	if strings.Contains(view, "test-clarify-model") {
		t.Fatalf("chatView should not render the footer while clarify is active")
	}
	if strings.Contains(view, "⏎ send · ctrl+j newline") {
		t.Fatalf("chatView should not render the composer while clarify is active")
	}

	wantPanelH := lipgloss.Height(a.clarifyPanel())
	wantVP := a.height - 2 - wantPanelH
	if wantVP < 5 {
		wantVP = 5
	}
	if a.vp.Height != wantVP {
		t.Fatalf("viewport height = %d, want %d (panel height %d)", a.vp.Height, wantVP, wantPanelH)
	}
}
