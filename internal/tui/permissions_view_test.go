package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestPermissionKnownToolNamesIncludesMutatingTools pins that the permission
// editor's known-tool set derives from Default().Names() and therefore picks
// Write and Edit up automatically — the editor must offer them, not silently
// omit them.
func TestPermissionKnownToolNamesIncludesMutatingTools(t *testing.T) {
	names := knownToolNames(t.TempDir())
	for _, want := range []string{"write", "edit", "read", "bash"} {
		if !names[want] {
			t.Fatalf("knownToolNames missing %q: %v", want, names)
		}
	}
}

func TestPermissionsViewGroupsByDecisionAndFitsATerminal(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.width, a.height = 80, 24
	a.push(viewPermissions)
	for i := 0; i < 30; i++ {
		if err := a.addPermissionRule("allow", fmt.Sprintf("Bash(cmd%d *)", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.addPermissionRule("deny", "Bash(rm *)"); err != nil {
		t.Fatal(err)
	}
	groups := a.permissionGroups(a.permissionRows())
	if len(groups) != 2 || groups[0].key != "deny" || groups[1].key != "allow" {
		t.Fatalf("groups = %+v", groups)
	}
	for _, sel := range []int{0, 15, 30} {
		a.permState.selected = sel
		out := a.View()
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > 24 {
			t.Fatalf("selected %d: %d lines\n%s", sel, len(lines), out)
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > 80 {
				t.Fatalf("line too wide: %q", l)
			}
		}
	}
	// ] moves to the next group's first rule; a row's idx is still its place in
	// permissionRows, so edit and delete keep addressing the right rule.
	a.permState.selected = 0
	m, _ := a.handlePermissionsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	a = m.(*App)
	if a.permState.selected != 1 {
		t.Fatalf("] selected %d, want the first allow rule (1)", a.permState.selected)
	}
}

func TestPermissionsViewEmptyStillRenders(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.width, a.height = 100, 30
	a.push(viewPermissions)
	if out := a.View(); !strings.Contains(out, "no rules") {
		t.Fatalf("empty list should say so:\n%s", out)
	}
}
