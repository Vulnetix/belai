package tui

import (
	"github.com/vulnetix/belai/internal/session"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/tui/components"
)

// resetPersonaAfter puts the brand palette back once a test is done: the
// persona colours are package state shared by every test in the package.
func resetPersonaAfter(t *testing.T) {
	t.Helper()
	t.Cleanup(components.ResetPersona)
}

// personaApp is an App in agent mode with the agent list loaded, as a session
// that has opened the picker is.
func personaApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	resetPersonaAfter(t)
	a := New(Options{Workdir: t.TempDir()})
	a.mode = "agent"
	a.loadAgents()
	if len(a.agents) == 0 {
		t.Fatal("no agents to choose from")
	}

	return a
}

// engageUntil presses ctrl+p until the footer names an agent, so the test
// walks the real cycle rather than setting the state it wants to see.
func engageUntil(t *testing.T, a *App, footer string) {
	t.Helper()
	for range len(a.agents) + 1 {
		if a.footer.Agent == footer {
			return
		}
		a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlP})
	}
	t.Fatalf("ctrl+p never reached %q; footer.Agent = %q", footer, a.footer.Agent)
}

// The footer chip names the agent by its display name, and the accent colours
// become the agent's own, in both terminal themes.
func TestEngagingAnAgentShowsItsDisplayNameAndColours(t *testing.T) {
	a := personaApp(t)
	brand := components.ColorTeal

	a.setNamedAgent("belai:patcher")
	a.refreshFooter()

	if a.footer.Agent != "Kremvax" {
		t.Fatalf("footer.Agent = %q, want the display name", a.footer.Agent)
	}
	if !strings.Contains(a.footer.View(), "Kremvax") {
		t.Fatalf("footer does not show the display name:\n%s", a.footer.View())
	}
	if components.ColorTeal == brand {
		t.Fatal("the accent did not change to the agent's primary")
	}
	// Kremvax's primary is #b3262e (a dark red): it is lightened on a dark
	// terminal until it reads, and kept as it is on a light one.
	if components.ColorTeal.Light != "#b3262e" {
		t.Fatalf("light accent = %s, want Kremvax's primary kept as is", components.ColorTeal.Light)
	}
	if components.ColorTeal.Dark == "#b3262e" {
		t.Fatal("dark accent was not lightened to read on a dark terminal")
	}
}

// ctrl+p cycles the agents and the colours follow live.
func TestCtrlPRethemesLive(t *testing.T) {
	a := personaApp(t)
	a.setNamedAgent("belai:patcher")
	a.refreshFooter()
	kremvax := components.ColorTeal
	gen := components.ThemeGeneration()

	engageUntil(t, a, "Kremvax") // already there: no key pressed
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlP})

	if a.footer.Agent == "Kremvax" {
		t.Fatal("ctrl+p did not move to another agent")
	}
	next := lookupAgentLook(a.namedAgent)
	if next.Display == "" {
		// A neighbour with no persona takes the colours off again.
		if components.ColorTeal == kremvax {
			t.Fatal("moving to an agent with no persona kept the previous agent's colours")
		}
	} else if a.footer.Agent != next.Display {
		t.Fatalf("footer.Agent = %q, want %q", a.footer.Agent, next.Display)
	}
	if components.ThemeGeneration() == gen {
		t.Fatal("the theme generation did not move, so cached rows would keep the old colours")
	}

	// Walk to Dark Avenger and check the colours are his, not a blend.
	engageUntil(t, a, "Dark Avenger")
	verifier := lookupAgentLook("belai:verifier")
	if got := components.ColorTeal.Light; got == kremvax.Light {
		t.Fatalf("Dark Avenger wears Kremvax's colours (%s)", got)
	}
	if !components.PersonaPalette(verifier.Palette) {
		t.Fatalf("built-in verifier has no usable palette: %v", verifier.Palette)
	}
}

// A profile with no persona, and clearing the agent, return the brand palette.
func TestAnAgentWithNoPersonaWearsTheBrandColours(t *testing.T) {
	a := personaApp(t)
	brand, brandSoft := components.ColorTeal, components.ColorTealSoft
	saveProfile(t, "reviewer")

	a.setNamedAgent("belai:patcher")
	a.refreshFooter()
	if components.ColorTeal == brand {
		t.Fatal("setup: Kremvax did not change the accent")
	}

	a.setNamedAgent("reviewer")
	a.refreshFooter()
	if components.ColorTeal != brand || components.ColorTealSoft != brandSoft {
		t.Fatal("a profile with no persona did not return the brand colours")
	}
	if a.footer.Agent != "reviewer" {
		t.Fatalf("footer.Agent = %q, want the profile name when there is no display name", a.footer.Agent)
	}

	a.setNamedAgent("belai:patcher")
	a.refreshFooter()
	a.clearEngagedAgent()
	if components.ColorTeal != brand {
		t.Fatal("clearing the agent did not return the brand colours")
	}
}

// An agent is dormant outside agent mode, and so are its colours.
func TestPersonaColoursFollowAgentMode(t *testing.T) {
	a := personaApp(t)
	brand := components.ColorTeal
	a.setNamedAgent("belai:patcher")
	a.refreshFooter()

	a.mode = "plan"
	a.refreshFooter()
	if components.ColorTeal != brand {
		t.Fatal("plan mode wore the dormant agent's colours")
	}
	a.mode = "agent"
	a.refreshFooter()
	if components.ColorTeal == brand {
		t.Fatal("returning to agent mode did not bring the agent's colours back")
	}
}

// A project that pinned an agent reopens wearing it, and a new App starts clean
// even when the last one left a persona on.
func TestNewAppStartsWithItsOwnPersona(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	resetPersonaAfter(t)
	brand := components.ColorTeal
	components.ApplyPersona([]string{"#b3262e", "#46566b", "#c86369", "#7a8594"})

	a := New(Options{Workdir: t.TempDir()})

	if a.personaFor != "" || components.ColorTeal != brand {
		t.Fatal("a new App inherited a persona it did not engage")
	}
}

// The switch notice names the agent both ways, the picker draws the display
// name with the role, and completing an argument keeps the names that are typed.
func TestPickerAndNoticeUseThePersona(t *testing.T) {
	a := personaApp(t)
	a.width = 600 // the strip is clipped to the window; keep every chip in view
	a.openAgentPicker()

	row := a.renderAgentPicker()
	if !strings.Contains(row, "Kremvax (patcher)") {
		t.Fatalf("picker row = %q, want the persona label", row)
	}

	a.agentArgSub = "start"
	a.agentArgCands = a.agents
	if row = a.renderAgentPicker(); !strings.Contains(row, "belai:patcher") || strings.Contains(row, "Kremvax") {
		t.Fatalf("argument picker row = %q, want the typed names", row)
	}
	a.agentArgSub, a.agentArgCands = "", nil

	engageUntil(t, a, "Kremvax")
	var found bool
	for _, m := range a.messages {
		if strings.Contains(m.Text(), "agent: Kremvax (belai:patcher)") {
			found = true
		}
	}
	if !found {
		t.Fatal("no notice said `agent: Kremvax (belai:patcher)`")
	}
}

func TestAgentLookLabels(t *testing.T) {
	k := lookupAgentLook("belai:patcher")
	if k.title("belai:patcher") != "Kremvax" || k.label("belai:patcher") != "Kremvax (patcher)" || k.notice("belai:patcher") != "Kremvax (belai:patcher)" {
		t.Fatalf("labels = %q %q %q", k.title("belai:patcher"), k.label("belai:patcher"), k.notice("belai:patcher"))
	}
	none := lookupAgentLook("no-such-agent")
	if none.title("x") != "x" || none.label("x") != "x" || none.notice("x") != "x" {
		t.Fatal("an agent with no persona must read as its name")
	}
	if got := lookupAgentLook(""); got.Display != "" || got.Palette != nil {
		t.Fatalf("no agent has no look, got %+v", got)
	}
}

// The agent a project pinned reopens wearing its colours: startup reads the
// pref and the first footer refresh dresses the TUI.
func TestAPinnedAgentIsWornFromStartup(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	resetPersonaAfter(t)
	workdir := t.TempDir()
	brand := components.ColorTeal

	first := New(Options{Workdir: workdir})
	first.mode = "agent"
	first.setNamedAgent("belai:patcher") // persists the project pref
	components.ResetPersona()

	second := New(Options{Workdir: workdir})
	if second.namedAgent != "belai:patcher" {
		t.Fatalf("namedAgent = %q, want the pinned agent", second.namedAgent)
	}
	if second.mode != "agent" {
		t.Skip("the project did not reopen in agent mode; the persona is dormant outside it")
	}
	if components.ColorTeal == brand || second.footer.Agent != "Kremvax" {
		t.Fatalf("a pinned agent was not worn from startup: footer %q", second.footer.Agent)
	}
}

// A new session starts with no agent engaged, so the colours go back too.
func TestANewSessionTakesThePersonaOff(t *testing.T) {
	a := personaApp(t)
	brand := components.ColorTeal
	a.setNamedAgent("belai:patcher")
	a.refreshFooter()
	if components.ColorTeal == brand {
		t.Fatal("setup: the persona did not apply")
	}

	a.startNewSession()

	if components.ColorTeal != brand {
		t.Fatal("a new session kept the previous agent's colours")
	}
	if a.footer.Agent == "Kremvax" {
		t.Fatalf("a new session kept the previous agent's name: %q", a.footer.Agent)
	}
}

// Resuming a session wears the agent it recorded, and resuming one that
// recorded none returns to the brand colours instead of keeping the last
// session's.
func TestResumeWearsTheRecordedAgentOrNone(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	resetPersonaAfter(t)
	a.mode = "agent"
	key, _ := session.KeyFor(workdir)
	brand := components.ColorTeal

	withAgent := append(basicSessionEntries(),
		session.Meta{ActiveProfile: "belai:verifier"}.ToEntry("a1"))
	seedEntries(t, a, key, "sess-agent", withAgent)
	seedEntries(t, a, key, "sess-plain", basicSessionEntries())

	a.resumeSession(key, "sess-agent")
	if a.namedAgent != "belai:verifier" || a.footer.Agent != "Dark Avenger" || components.ColorTeal == brand {
		t.Fatalf("resume did not wear the recorded agent: %q footer %q", a.namedAgent, a.footer.Agent)
	}

	a.resumeSession(key, "sess-plain")
	if a.namedAgent != "" {
		t.Fatalf("namedAgent = %q after resuming a session with no agent", a.namedAgent)
	}
	if components.ColorTeal != brand {
		t.Fatal("resuming a session with no agent kept the previous session's colours")
	}
}

// In auto mode the chip names the mode the classifier picked, not the agent,
// but the colours still follow the engaged agent. While an approved plan runs
// the chip says so, and the agent's colours stay.
func TestChipOverridesNeverHideThePersonaColours(t *testing.T) {
	a := personaApp(t)
	brand := components.ColorTeal
	a.setNamedAgent("belai:patcher")

	a.modeAuto = true
	a.refreshFooter()
	if a.footer.Agent != a.mode {
		t.Fatalf("auto mode chip = %q, want the classified mode %q", a.footer.Agent, a.mode)
	}
	if components.ColorTeal == brand {
		t.Fatal("auto mode dropped the engaged agent's colours")
	}
	a.modeAuto = false

	a.planExecuting = true
	a.refreshFooter()
	if a.footer.Agent != "executing" || components.ColorTeal == brand {
		t.Fatalf("plan execution: chip %q, colours changed %v", a.footer.Agent, components.ColorTeal != brand)
	}
}

// A flat profile that shares a name with an agent definition (belai:debug) is
// one agent to the person using it: it wears the definition's persona, and a
// flat profile with no definition has none.
func TestAFlatProfileWearsTheDefinitionOfTheSameName(t *testing.T) {
	a := personaApp(t)
	saveProfile(t, "reviewer")

	a.setNamedAgent("belai:debug")
	a.refreshFooter()
	if a.footer.Agent != "Pip Ostrander" {
		t.Fatalf("belai:debug chip = %q, want its definition's display name", a.footer.Agent)
	}
	a.setNamedAgent("reviewer")
	a.refreshFooter()
	if a.footer.Agent != "reviewer" {
		t.Fatalf("a flat profile with no definition shows %q, want its name", a.footer.Agent)
	}
}
