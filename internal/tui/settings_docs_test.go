package tui

import (
	"fmt"
	"regexp"
	"sort"
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
		case strings.HasPrefix(r.key, "knowledge."):
			named = strings.Contains(doc, "`knowledge …`")
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

// TestSettingsPageListsEachRowUnderItsGroup keeps the Groups table right about
// membership, not only presence: the labels in a group's row are exactly the
// rows the screen puts in that group.
func TestSettingsPageListsEachRowUnderItsGroup(t *testing.T) {
	doc := docparity.Read(t, "docs/settings.md")
	a := jevThresholdApp(t)
	byGroup := map[string][]string{}
	for _, r := range a.settingsRows() {
		byGroup[r.group] = append(byGroup[r.group], r.label)
	}
	label := regexp.MustCompile("`([^`]+)`")
	for _, d := range settingsGroupDefs {
		row := regexp.MustCompile("(?m)^\\| " + regexp.QuoteMeta(d.title) + " \\| (.*) \\|$").FindStringSubmatch(doc)
		if row == nil {
			t.Errorf("no Groups table row for %q", d.title)
			continue
		}
		var listed []string
		for _, m := range label.FindAllStringSubmatch(row[1], -1) {
			listed = append(listed, m[1])
		}
		if d.key == "jevthresholds" {
			// One slider per cut-off; the sections table lists them.
			continue
		}
		want := byGroup[d.key]
		sort.Strings(listed)
		sort.Strings(want)
		if strings.Join(listed, "|") != strings.Join(want, "|") {
			t.Errorf("group %q: the page lists %v, the screen has %v", d.title, listed, want)
		}
	}
	// Every threshold is in exactly one section of the sections table.
	for _, r := range a.settingsRows() {
		if r.group != "jevthresholds" {
			continue
		}
		if n := strings.Count(doc, "`"+r.label+"`"); n < 1 {
			t.Errorf("the threshold %q is not in the sections table", r.label)
		}
	}
}

// TestSettingsPageListsEachCutOffUnderItsSection keeps the sections table right
// about which cut-off belongs to which section, and in the same order.
func TestSettingsPageListsEachCutOffUnderItsSection(t *testing.T) {
	doc := docparity.Read(t, "docs/settings.md")
	label := regexp.MustCompile("`([^`]+)`")
	for _, sec := range jevThresholdSections {
		row := regexp.MustCompile("(?m)^\\| " + regexp.QuoteMeta(sec.title) + " \\| (.*) \\|$").FindStringSubmatch(doc)
		if row == nil {
			t.Errorf("no sections table row for %q", sec.title)
			continue
		}
		var listed, want []string
		for _, m := range label.FindAllStringSubmatch(row[1], -1) {
			listed = append(listed, m[1])
		}
		for _, k := range sec.keys {
			want = append(want, jevThresholdLabels[k])
		}
		if strings.Join(listed, "|") != strings.Join(want, "|") {
			t.Errorf("section %q: the page lists %v, the code has %v", sec.title, listed, want)
		}
	}
	// The sections table is in the order the screen shows.
	last := -1
	for _, sec := range jevThresholdSections {
		i := strings.Index(doc, "\n| "+sec.title+" |")
		if i < last {
			t.Errorf("the section %q is out of order on the page", sec.title)
		}
		last = i
	}
}
