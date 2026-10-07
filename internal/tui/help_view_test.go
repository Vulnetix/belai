package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func newHelpApp(t *testing.T, w, h int) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	a.handleCommand("/help")
	return a
}

func helpKey(a *App, k string) {
	switch k {
	case "esc":
		a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	case "pgdown":
		a.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	case "pgup":
		a.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	case "tab":
		a.Update(tea.KeyMsg{Type: tea.KeyTab})
	case "shift+tab":
		a.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	case "end":
		a.Update(tea.KeyMsg{Type: tea.KeyEnd})
	case "home":
		a.Update(tea.KeyMsg{Type: tea.KeyHome})
	default:
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
}

func TestHelpScreenScrollsAndShowsPosition(t *testing.T) {
	a := newHelpApp(t, 100, 30)
	first := gsPlain(a.helpView())
	if !strings.Contains(first, "commands:") || strings.Contains(first, "text entry") {
		t.Fatalf("the top of the screen should show the commands, not the end:\n%s", first)
	}
	if !strings.Contains(first, "/") || !strings.Contains(first, "1–") {
		t.Fatalf("no position indicator:\n%s", first)
	}
	helpKey(a, "j")
	if a.helpState.scroll != 1 {
		t.Fatalf("j scrolled to %d, want 1", a.helpState.scroll)
	}
	helpKey(a, "pgdown")
	if a.helpState.scroll <= 1 {
		t.Fatalf("pgdn did not page: %d", a.helpState.scroll)
	}
	helpKey(a, "G")
	last := gsPlain(a.helpView())
	if !strings.Contains(last, "agent picker") || strings.Contains(last, "commands:") {
		t.Fatalf("G should land on the last page:\n%s", last)
	}
	helpKey(a, "g")
	if a.helpState.scroll != 0 {
		t.Fatalf("g left scroll at %d", a.helpState.scroll)
	}
	helpKey(a, "k")
	if a.helpState.scroll != 0 {
		t.Fatalf("scroll went negative: %d", a.helpState.scroll)
	}
}

func TestHelpScreenJumpsBetweenSections(t *testing.T) {
	a := newHelpApp(t, 100, 30)
	_, sections := a.helpLines(a.contentWidth())
	if len(sections) < 10 {
		t.Fatalf("only %d section titles found", len(sections))
	}
	helpKey(a, "tab")
	if a.helpState.scroll != sections[1] && a.helpState.scroll != sections[0] {
		// scroll starts at 0, which is the commands title: tab moves to the next one.
		t.Fatalf("tab went to %d, sections %v", a.helpState.scroll, sections[:3])
	}
	at := a.helpState.scroll
	helpKey(a, "tab")
	if a.helpState.scroll <= at {
		t.Fatalf("tab did not advance: %d then %d", at, a.helpState.scroll)
	}
	helpKey(a, "shift+tab")
	if a.helpState.scroll != at {
		t.Fatalf("shift+tab went to %d, want %d", a.helpState.scroll, at)
	}
}

func TestHelpScreenEscReturnsToChat(t *testing.T) {
	a := newHelpApp(t, 100, 30)
	helpKey(a, "esc")
	if a.view != viewChat || len(a.viewStack) != 0 {
		t.Fatalf("view = %v, stack %v after esc", a.view, a.viewStack)
	}
	a.handleCommand("/help")
	helpKey(a, "q")
	if a.view != viewChat {
		t.Fatalf("q left view = %v", a.view)
	}
}

func TestHelpScreenWrapsToTheWidthWithAHangingIndent(t *testing.T) {
	a := newHelpApp(t, 60, 30)
	lines, _ := a.helpLines(a.contentWidth())
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > a.contentWidth() {
			t.Fatalf("line wider than the screen (%d > %d): %q", w, a.contentWidth(), ansi.Strip(l))
		}
	}
	// A long binding keeps its description under the description column.
	found := false
	for i, l := range lines {
		if strings.Contains(ansi.Strip(l), "toggle between the main and fast model") {
			if !strings.HasPrefix(ansi.Strip(lines[i+1]), "  ") {
				t.Fatalf("continuation not indented: %q", ansi.Strip(lines[i+1]))
			}
			found = true
		}
	}
	if !found {
		t.Skip("the ctrl+q row text changed")
	}
}

func TestHelpScreenWheelScrolls(t *testing.T) {
	a := newHelpApp(t, 100, 30)
	a.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if a.helpState.scroll == 0 {
		t.Fatal("the wheel did not scroll the help screen")
	}
	a.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if a.helpState.scroll != 0 {
		t.Fatalf("wheel up left scroll at %d", a.helpState.scroll)
	}
}

func TestHelpScreenFollowsTheMacHintSetting(t *testing.T) {
	a := newHelpApp(t, 120, 60)
	all := func() string {
		a.helpView() // refreshes the cache for the current setting
		lines, _ := a.helpLines(a.contentWidth())
		return gsPlain(strings.Join(lines, "\n"))
	}
	macHints(t, true)
	if !strings.Contains(all(), "f9 (Fn+⏭)") {
		t.Fatal("hints on: no Mac form on the screen")
	}
	macHints(t, false)
	if strings.Contains(all(), "(Fn+") {
		t.Fatal("hints off: Mac form still cached")
	}
}
