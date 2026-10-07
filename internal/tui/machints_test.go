package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/tui/components"
)

func macHints(t *testing.T, on bool) {
	t.Helper()
	prev := components.MacKeyHintsOn()
	components.SetMacKeyHints(on)
	t.Cleanup(func() { components.SetMacKeyHints(prev) })
}

func TestHelpTextCarriesMacHintsOnlyWhenOn(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	macHints(t, true)
	on := helpText(a.registry)
	for _, want := range []string{"f9 (Fn+⏭)", "f3 (Fn+Mission Control)", "f10 (Fn+mute)"} {
		if !strings.Contains(on, want) {
			t.Errorf("help is missing %q", want)
		}
	}
	macHints(t, false)
	if off := helpText(a.registry); strings.Contains(off, "(Fn+") {
		t.Errorf("hints leaked into help with the setting off")
	}
}

func TestHelpRowsStayAlignedWithMacHints(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	macHints(t, true)
	cols := map[int]bool{}
	section := ""
	for _, line := range strings.Split(helpText(a.registry), "\n") {
		if line != "" && !strings.HasPrefix(line, " ") {
			section = line
			continue
		}
		if section != "anywhere:" {
			continue
		}
		if i := strings.Index(line, " — "); i >= 0 {
			cols[len([]rune(line[:i]))] = true
		}
	}
	if len(cols) != 1 {
		t.Fatalf("the anywhere rows use %d dash columns, want 1", len(cols))
	}
}

func TestWorkingHintShowsMacFormForAnFKeyTip(t *testing.T) {
	macHints(t, true)
	for i := 0; i < components.TipCount(); i++ {
		tip := components.CycleTip("seed", i)
		if strings.HasPrefix(tip, "f9 ") {
			if !strings.HasPrefix(tip, "f9 (Fn+⏭) ") {
				t.Fatalf("tip %q lacks the Mac form", tip)
			}
			return
		}
	}
	t.Fatal("no f9 tip in the table")
}

func TestHelpBarShowsMacFormForFKeycaps(t *testing.T) {
	macHints(t, true)
	if got := components.HelpBar("f9", "runs"); !strings.Contains(got, "f9 (Fn+⏭)") {
		t.Fatalf("got %q", got)
	}
	if got := components.HelpBar("ctrl+x", "copy f9"); strings.Contains(got, "ctrl+x (Fn+") {
		t.Fatalf("a non F keycap changed: %q", got)
	}
	macHints(t, false)
	if got := components.HelpBar("f9", "runs"); strings.Contains(got, "Fn+") {
		t.Fatalf("hint with the setting off: %q", got)
	}
}
