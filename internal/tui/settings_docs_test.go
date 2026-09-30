package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestSettingsPageDocumentsEveryGroupRowAndSection keeps docs/settings.md in
// step with the rail, the rows and the threshold sections.
func TestSettingsPageDocumentsEveryGroupRowAndSection(t *testing.T) {
	doc := docparity.Read(t, "docs/settings.md")
	a := jevThresholdApp(t) // a decision backend is set, so every group exists
	for _, d := range settingsGroupDefs {
		if !strings.Contains(doc, "| "+d.title+" |") {
			t.Errorf("docs/settings.md has no table row for the group %q", d.title)
		}
	}
	for _, r := range a.settingsRows() {
		if !strings.Contains(doc, "`"+r.label+"`") {
			t.Errorf("docs/settings.md does not document the row %q", r.label)
		}
	}
	for _, sec := range jevThresholdSections {
		if !strings.Contains(doc, "| "+sec.title+" |") {
			t.Errorf("docs/settings.md has no row for the threshold section %q", sec.title)
		}
	}
}

// TestSettingsPageStatesTheLayoutConstants pins the numbers the page quotes.
func TestSettingsPageStatesTheLayoutConstants(t *testing.T) {
	doc := docparity.Read(t, "docs/settings.md")
	for _, want := range []string{
		fmt.Sprintf("%d columns or more", glSideBySide),
		fmt.Sprintf("under %d columns", glSideBySide),
		fmt.Sprintf("holds up to %d lines", glDetailRows),
		fmt.Sprintf("never falls under %d rows", glMinBody),
		fmt.Sprintf("%d cells with a `│`", sliderTrackCells),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/settings.md does not say %q", want)
		}
	}
}

// TestSettingsPageListsEveryGlobalOnlyRow keeps the "saved to global" rule in
// the page in step with settingsWritesGlobalOnly.
func TestSettingsPageListsEveryGlobalOnlyRow(t *testing.T) {
	doc := docparity.Read(t, "docs/settings.md")
	a := jevThresholdApp(t)
	for _, r := range a.settingsRows() {
		if !settingsWritesGlobalOnly(r.key) {
			continue
		}
		var named bool
		switch {
		case r.key == "auto_commit_per_task", r.key == "plan_limits":
			named = strings.Contains(doc, "`"+r.label+"`")
		case strings.HasPrefix(r.key, "tests."):
			named = strings.Contains(doc, "every `test …`")
		case strings.HasPrefix(r.key, "voice."):
			named = strings.Contains(doc, "`voice …`")
		case strings.HasPrefix(r.key, "tts."):
			named = strings.Contains(doc, "`read aloud …`")
		default:
			named = strings.Contains(doc, "every Jev threshold")
		}
		if !named {
			t.Errorf("docs/settings.md does not say %q (%s) is saved to global only", r.label, r.key)
		}
	}
}

// TestGroupedListIsDocumentedInTheDesignSystem keeps docs/tui-design.md's
// grouped-list section and the README index pointing at the page.
func TestGroupedListIsDocumentedInTheDesignSystem(t *testing.T) {
	design := docparity.Read(t, "docs/tui-design.md")
	for _, want := range []string{"### Grouped lists", "glHelpLine", "grouplist.go"} {
		if !strings.Contains(design, want) {
			t.Errorf("docs/tui-design.md does not mention %q", want)
		}
	}
	index := docparity.Read(t, "docs/README.md")
	if !strings.Contains(index, "settings.md") {
		t.Error("docs/README.md does not link docs/settings.md")
	}
}
