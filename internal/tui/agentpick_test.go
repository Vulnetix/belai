package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/profiles"
)

// saveProfile writes a user profile into the test's SIGNET_HOME.
func saveProfile(t *testing.T, name string) {
	t.Helper()
	if _, err := profiles.Save(profiles.Profile{Name: name, Content: "you are " + name}); err != nil {
		t.Fatalf("Save(%q): %v", name, err)
	}
}

// The picker offers built-ins first (signet:debug at index 0), then the
// user's profiles, and marks which is which so a harness profile is never
// mistaken for a file the user wrote.
func TestAgentPickerListsBuiltinsFirstThenProfiles(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")

	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()

	if len(a.agents) < 2 {
		t.Fatalf("agents = %+v, want at least two", a.agents)
	}
	if a.agents[0].Name != profiles.DebugProfile {
		t.Fatalf("first agent = %q, want %q", a.agents[0].Name, profiles.DebugProfile)
	}

	a.openAgentPicker()
	if !a.agentPickerVisible() {
		t.Fatalf("expected the picker to show in agent mode")
	}

	row := a.renderAgentPicker()
	if !strings.Contains(row, "reviewer") || !strings.Contains(row, profiles.DebugProfile) {
		t.Fatalf("picker row = %q, want both profiles", row)
	}
	if !strings.Contains(row, "◈") {
		t.Fatalf("picker row = %q, want the built-in marker", row)
	}
}

// The picker is agent-mode chrome, and it yields the strip — and tab — to the
// slash popup when both could show. It is also hidden while a file @-prefix is
// active.
func TestAgentPickerHiddenOutsideAgentModeAndUnderSlashPopup(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()

	a.mode = "plan"
	if a.agentPickerVisible() {
		t.Fatalf("expected the picker to be hidden in plan mode")
	}

	a.mode = "agent"
	a.editor.SetValue("/c")
	a.refreshAutocomplete()
	if a.agentPickerVisible() {
		t.Fatalf("expected the slash popup to win")
	}

	a.clearAutocomplete()
	a.autocompleteIndex = noAutocompleteSelection
	a.editor.SetValue("@go")
	a.editor.CursorEnd()
	setFileList(a, "foo.go")
	if !a.filePickerVisible() {
		t.Fatalf("expected the file chooser to show for @go")
	}
	if a.agentPickerVisible() {
		t.Fatalf("expected the file chooser to hide the agent picker")
	}
}

// Typing @agent: does not open the agent picker; it is now a file-chooser
// prefix (or a prompt-level directive handled by the classifier).
func TestAgentPickerNotOpenedByAtPrefix(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.editor.SetValue("@agent:")
	a.editor.CursorEnd()

	if a.agentPickerVisible() {
		t.Fatalf("@agent: should not open the agent picker")
	}
}

// Tab walks every candidate and then the (none) entry, wrapping back to the
// first — and never writes into the prompt.
func TestAgentPickerTabCyclesThroughNone(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	a.agentIndex = noAgentSelection // start cold, the way a fresh /agent behaves
	original := a.editor.Value()
	want := a.agents

	for i := range want {
		a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
		got, ok := a.agentSelection()
		if !ok || got.Name != want[i].Name {
			t.Fatalf("tab %d: selection = %+v (ok=%v), want %q", i+1, got, ok, want[i].Name)
		}
		if a.editor.Value() != original {
			t.Fatalf("tab %d: prompt = %q, want it untouched", i+1, a.editor.Value())
		}
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if got, _ := a.agentSelection(); got.Name != agentNoneLabel {
		t.Fatalf("selection = %q, want the (none) entry", got.Name)
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if got, _ := a.agentSelection(); got.Name != want[0].Name {
		t.Fatalf("selection after wrap = %q, want %q", got.Name, want[0].Name)
	}
}

// Enter on a highlighted agent engages it instead of sending the turn, and
// the picker closes.
func TestAgentPickerEnterEngagesProfile(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	a.editor.SetValue("fix the flaky test")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	want, _ := a.agentSelection()

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.namedAgent != want.Name {
		t.Fatalf("namedAgent = %q, want %q", a.namedAgent, want.Name)
	}
	if a.editor.Value() != "fix the flaky test" {
		t.Fatalf("prompt = %q, want the typed text kept", a.editor.Value())
	}
	if a.agentPickerOpen {
		t.Fatalf("picker should be closed after accepting")
	}
	a.refreshFooter()
	if a.footer.Agent != want.Name {
		t.Fatalf("footer.Agent = %q, want %q", a.footer.Agent, want.Name)
	}
	if !strings.Contains(a.footer.View(), want.Name) {
		t.Fatalf("footer does not show the engaged agent:\n%s", a.footer.View())
	}
}

// Right accepts like enter, but only once something is highlighted — otherwise
// it is the editor's cursor key.
func TestAgentPickerRightIsCursorUntilHighlighted(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	a.agentIndex = noAgentSelection // no default highlight
	a.editor.SetValue("abc")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRight})
	if a.namedAgent != "" {
		t.Fatalf("namedAgent = %q, want nothing engaged", a.namedAgent)
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRight})
	if a.namedAgent == "" {
		t.Fatalf("expected right to engage the highlighted agent")
	}
}

// Enter in agent mode with no agent engaged opens the picker rather than
// sending. The default signet:debug profile is selected.
func TestEnterInAgentModeWithoutAgentOpensPicker(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.mode = "agent"
	a.editor.SetValue("hello")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if !a.agentPickerOpen {
		t.Fatalf("expected the agent picker to open")
	}
	if got, _ := a.agentSelection(); got.Name != profiles.DebugProfile {
		t.Fatalf("selection = %q, want %q", got.Name, profiles.DebugProfile)
	}
	if a.editor.Value() != "hello" {
		t.Fatalf("prompt should not be cleared, got %q", a.editor.Value())
	}
}

// The /agent command with no argument opens the agent picker.
func TestAgentCommandOpensPicker(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.handleCommand("/agent")

	if !a.agentPickerOpen {
		t.Fatalf("expected /agent to open the picker")
	}
	if got, _ := a.agentSelection(); got.Name != profiles.DebugProfile {
		t.Fatalf("selection = %q, want %q", got.Name, profiles.DebugProfile)
	}
}

// /agent tolerates trailing whitespace and still opens the picker.
func TestAgentCommandWithTrailingSpaceOpensPicker(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.handleCommand("/agent   ")

	if !a.agentPickerOpen {
		t.Fatalf("expected '/agent   ' to open the picker")
	}
}

// /agent with a subcommand still dispatches to background-agent management.
func TestAgentCommandWithSubcommandDoesNotOpenPicker(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.handleCommand("/agent list")

	if a.agentPickerOpen {
		t.Fatalf("expected /agent list to dispatch, not open the picker")
	}
	if a.view != viewAgent {
		t.Fatalf("view = %q, want agent list view", a.view)
	}
}

// Esc closes the picker and leaves the composer untouched.
func TestAgentPickerEscCloses(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	a.editor.SetValue("question")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})

	if a.agentPickerOpen {
		t.Fatalf("expected esc to close the picker")
	}
	if a.editor.Value() != "question" {
		t.Fatalf("prompt = %q, want it untouched", a.editor.Value())
	}
}

// When the picker is open, ordinary typing preserves the highlighted agent
// rather than resetting it, so the selection stays stable while the user
// edits the prompt.
func TestAgentPickerTypingPreservesHighlight(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})

	if !a.agentPickerOpen {
		t.Fatalf("expected the picker to stay open while typing")
	}
	if got, _ := a.agentSelection(); got.Name != profiles.DebugProfile {
		t.Fatalf("selection = %q, want it preserved", got.Name)
	}
}

// The (none) entry clears the engaged agent, so the picker can undo itself.
func TestAgentPickerNoneClearsSelection(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	a.namedAgent = "reviewer"
	a.agentIndex = len(a.agentCandidates()) // the (none) slot

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.namedAgent != "" {
		t.Fatalf("namedAgent = %q, want it cleared", a.namedAgent)
	}
}

// The engaged profile carries the turn: it reaches the agent loop as
// ForceAgent, which is what swaps the system prompt's carrier block.
func TestEngagedAgentReachesTurnInput(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.namedAgent == "" {
		t.Fatalf("expected an engaged agent")
	}
	// send() is what copies namedAgent into TurnInput.ForceAgent; the field is
	// read there for every turn, so the engaged profile is not per-message.
	if a.namedAgent != a.agents[0].Name {
		t.Fatalf("namedAgent = %q, want %q", a.namedAgent, a.agents[0].Name)
	}
}

// A new session starts with no engaged agent: the profile is session state.
func TestNewSessionClearsEngagedAgent(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: t.TempDir()})
	a.namedAgent = "reviewer"

	a.startNewSession()

	if a.namedAgent != "" {
		t.Fatalf("namedAgent = %q, want it cleared by a new session", a.namedAgent)
	}
}

// saveBackgroundAgent writes an internal/agentprofile definition into the
// test's SIGNET_HOME.
func saveBackgroundAgent(t *testing.T, name string, tools ...string) {
	t.Helper()
	p := agentprofile.AgentProfile{
		Name:         name,
		Description:  name + " definition",
		SystemPrompt: "you are " + name,
		Mode:         agentprofile.ModeSingle,
		Tools:        tools,
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatalf("Save(%q): %v", name, err)
	}
}

// Background-agent definitions are offered alongside the flat profiles, marked
// so the two kinds are distinguishable.
func TestAgentPickerListsBackgroundDefinitions(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	saveBackgroundAgent(t, "nightly-audit", "Read", "Grep")

	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()

	var bg *agentChoice
	for i := range a.agents {
		if a.agents[i].Name == "nightly-audit" {
			bg = &a.agents[i]
		}
	}
	if bg == nil {
		t.Fatalf("agents = %+v, want the background definition", a.agents)
	}
	if !bg.Background || bg.Builtin {
		t.Fatalf("choice = %+v, want Background", *bg)
	}
	if len(bg.Tools) != 2 {
		t.Fatalf("Tools = %v, want the definition's allowlist", bg.Tools)
	}
	if row := a.renderAgentPicker(); !strings.Contains(row, "↻ nightly-audit") {
		t.Fatalf("picker row = %q, want the background marker", row)
	}
}

// A flat profile owns a shared name: it is what CarrierOptions resolves first,
// so the shadowed definition must not be offered as a separate row.
func TestAgentPickerFlatProfileWinsASharedName(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	saveBackgroundAgent(t, "reviewer")

	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()

	var count int
	for _, c := range a.agents {
		if c.Name == "reviewer" {
			count++
			if c.Background {
				t.Fatalf("the background definition shadowed the flat profile")
			}
		}
	}
	if count != 1 {
		t.Fatalf("reviewer appears %d times, want 1", count)
	}
}

// Engaging a background definition carries its prompt here and narrows the
// session's tools to its allowlist.
func TestAgentPickerEngagingBackgroundDefinitionAppliesTools(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveBackgroundAgent(t, "nightly-audit", "Read", "Grep")

	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	for {
		c, _ := a.agentSelection()
		if c.Name == "nightly-audit" {
			break
		}
		a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.namedAgent != "nightly-audit" {
		t.Fatalf("namedAgent = %q, want nightly-audit", a.namedAgent)
	}
	if len(a.namedAgentTools) != 2 {
		t.Fatalf("namedAgentTools = %v, want the definition's allowlist", a.namedAgentTools)
	}
	if got := a.sessionBuildParams().toolAllow; len(got) != 2 {
		t.Fatalf("toolAllow = %v, want it carried into the session build", got)
	}
}

// ctrl+g starts the highlighted definition as a background agent instead of
// engaging it, and says so when the row cannot be started.
func TestAgentPickerCtrlGStartsOnlyBackgroundDefinitions(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")

	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if got, _ := a.agentSelection(); got.Name != "reviewer" {
		t.Fatalf("selection = %+v, want reviewer", got)
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlG})

	if a.namedAgent != "" {
		t.Fatalf("ctrl+g engaged %q; it must not engage", a.namedAgent)
	}
	last := a.messages[len(a.messages)-1].Text()
	if !strings.Contains(last, "not a background agent") {
		t.Fatalf("message = %q, want the flat-profile explanation", last)
	}
}

// Plan and goal mode carry Signet's own plan or goal, and the system prompt
// holds exactly one carrier: an engaged agent must go quiet there — hidden
// from the footer, absent from the turn, and not narrowing the tools.
func TestEngagedAgentIsDormantOutsideAgentMode(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveBackgroundAgent(t, "nightly-audit", "Read")
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.openAgentPicker()
	for {
		c, _ := a.agentSelection()
		if c.Name == "nightly-audit" {
			break
		}
		a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.engagedAgent() != "nightly-audit" {
		t.Fatalf("engagedAgent = %q, want it engaged in agent mode", a.engagedAgent())
	}

	for _, mode := range []string{"plan", "goal"} {
		a.mode = mode
		if got := a.engagedAgent(); got != "" {
			t.Errorf("%s mode: engagedAgent = %q, want nothing carried", mode, got)
		}
		if got := a.engagedAgentTools(); got != nil {
			t.Errorf("%s mode: toolAllow = %v, want the mode's own registry", mode, got)
		}
		a.refreshFooter()
		if a.footer.Agent != "" {
			t.Errorf("%s mode: footer shows %q", mode, a.footer.Agent)
		}
		if a.agentPickerVisible() {
			t.Errorf("%s mode: the picker is still showing", mode)
		}
	}

	// Dormant, not discarded: cycling back restores the selection rather than
	// making the user pick it again.
	a.mode = "agent"
	if a.engagedAgent() != "nightly-audit" {
		t.Fatalf("engagedAgent = %q, want it back in agent mode", a.engagedAgent())
	}
	if len(a.engagedAgentTools()) != 1 {
		t.Fatalf("toolAllow = %v, want the allowlist back", a.engagedAgentTools())
	}
}

// Cycling the mode away from agent must not send the engaged agent, which
// would force the turn back into agent mode and drop the mode the user chose.
func TestCyclingModeStopsSendingTheEngagedAgent(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	saveProfile(t, "reviewer")
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.namedAgent = "reviewer"

	a.cycleMode() // agent → plan

	if a.mode != "plan" {
		t.Fatalf("mode = %q, want plan", a.mode)
	}
	if a.engagedAgent() != "" {
		t.Fatalf("plan mode would still send %q as ForceAgent", a.engagedAgent())
	}
}

// Enter opens the picker, and the next enter engages the highlighted agent
// AND sends the prompt that was waiting in the composer. Without this the
// submit is swallowed twice with no feedback: the prompt sits in the editor
// while the user waits for a turn that never started.
func TestAcceptingAgentSendsThePromptThatOpenedThePicker(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.mode = "agent"
	a.editor.SetValue("hello")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}) // opens the picker
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}) // engages, and must send

	if a.namedAgent != profiles.DebugProfile {
		t.Fatalf("namedAgent = %q, want %q", a.namedAgent, profiles.DebugProfile)
	}
	if a.editor.Value() != "" {
		t.Fatalf("prompt not submitted, editor still holds %q", a.editor.Value())
	}
	var sent bool
	for _, m := range a.messages {
		if m.Role == "user" && m.Text() == "hello" {
			sent = true
		}
	}
	if !sent {
		t.Fatalf("prompt was never echoed as a user turn: %+v", a.messages)
	}
}

// The picker opened by /agent is not a pending submit: engaging an agent
// there must not send whatever happens to be in the composer.
func TestAgentCommandPickerDoesNotSendComposerText(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.mode = "agent"
	a.editor.SetValue("draft I am still writing")

	a.handleCommand("/agent")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.editor.Value() != "draft I am still writing" {
		t.Fatalf("composer was consumed, got %q", a.editor.Value())
	}
	for _, m := range a.messages {
		if m.Role == "user" {
			t.Fatalf("unexpected user turn: %+v", a.messages)
		}
	}
}

// (none) leaves agent mode without a carrier — the exact state the picker
// exists to prevent — so it must never send the pending submit.
func TestNoneSelectionDoesNotSendPendingSubmit(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.mode = "agent"
	a.editor.SetValue("hello")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}) // opens the picker
	a.agentIndex = len(a.agentCandidates())         // highlight (none)
	if got, _ := a.agentSelection(); got.Name != agentNoneLabel {
		t.Fatalf("selection = %q, want %q", got.Name, agentNoneLabel)
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.namedAgent != "" {
		t.Fatalf("namedAgent = %q, want none engaged", a.namedAgent)
	}
	if a.editor.Value() != "hello" {
		t.Fatalf("prompt was consumed, editor holds %q", a.editor.Value())
	}
	assertNoUserTurn(t, a)
}

// esc cancels the pending submit along with the highlight: the prompt stays
// in the composer and must not be sent by a later, unrelated engage.
func TestEscapeCancelsThePendingSubmit(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.mode = "agent"
	a.editor.SetValue("hello")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}) // opens the picker
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})   // cancels it
	if a.agentPickerSubmit {
		t.Fatalf("esc left the submit pending")
	}

	a.handleCommand("/agent") // an unrelated opening
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.namedAgent == "" {
		t.Fatalf("expected the agent to engage")
	}
	if a.editor.Value() != "hello" {
		t.Fatalf("prompt was consumed, editor holds %q", a.editor.Value())
	}
	assertNoUserTurn(t, a)
}

// A prompt left unsent in one session must never be sent into another, so
// starting a new session drops the pending submit with the picker state.
func TestNewSessionDropsThePendingSubmit(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.mode = "agent"
	a.editor.SetValue("hello")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}) // opens the picker
	a.startNewSession()

	if a.agentPickerSubmit {
		t.Fatalf("a new session kept the pending submit")
	}
	if a.agentPickerOpen {
		t.Fatalf("a new session kept the picker open")
	}
}

// Resuming another session drops the pending submit for the same reason.
func TestResumeDropsThePendingSubmit(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.mode = "agent"
	a.editor.SetValue("hello")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}) // opens the picker
	a.clearForResume()

	if a.agentPickerSubmit {
		t.Fatalf("resume kept the pending submit")
	}
}

// An empty composer is not a submit: enter opens the picker to choose a
// carrier, and engaging one must not start a turn with no prompt.
func TestEmptyComposerNeverArmsThePendingSubmit(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.loadAgents()
	a.mode = "agent"

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}) // opens the picker
	if a.agentPickerSubmit {
		t.Fatalf("an empty composer armed a submit")
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.namedAgent == "" {
		t.Fatalf("expected the agent to engage")
	}
	assertNoUserTurn(t, a)
}

func assertNoUserTurn(t *testing.T, a *App) {
	t.Helper()
	for _, m := range a.messages {
		if m.Role == "user" {
			t.Fatalf("unexpected user turn: %+v", a.messages)
		}
	}
}
