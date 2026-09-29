package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/rolemanager"
)

func rmTestMsg(summary string, tone rolemanager.Tone, meta RMMeta, ms int64) Message {
	return Message{
		Role:       "rolemanager",
		RM:         rolemanager.Description{Summary: summary, Outcome: "cleared it", Tone: tone},
		RMMeta:     meta,
		DurationMS: ms,
	}
}

func plainRow(segs []Seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

var jevMeta = RMMeta{ActorKind: "jev", Actor: "jev-1.13", Category: "security", Icon: "shield"}

func TestRMRowIsOneLineAtEveryWidth(t *testing.T) {
	long := strings.Repeat("Checked what the Bash command would do before running it ", 4)
	for _, w := range []int{40, 60, 80, 120} {
		for _, meta := range []RMMeta{jevMeta, {ActorKind: "harness", Actor: "belai", Category: "context", Icon: "recall", Cause: "cache_hit"}, {}} {
			row := plainRow(rmRowSegs(rmTestMsg(long, rolemanager.ToneClear, meta, 412), w))
			if strings.Contains(row, "\n") {
				t.Fatalf("width %d: row wraps: %q", w, row)
			}
			if got := lipgloss.Width(row); got > w {
				t.Errorf("width %d: row is %d cells: %q", w, got, row)
			}
			if !strings.Contains(row, "cleared it") {
				t.Errorf("width %d: the outcome was cut: %q", w, row)
			}
		}
	}
}

func TestRMRowKeepsDeciderAndMarkers(t *testing.T) {
	row := plainRow(rmRowSegs(rmTestMsg("Checked the command", rolemanager.ToneClear, jevMeta, 412), 120))
	for _, want := range []string{"◈ ", "Checked the command", "cleared it ✓", "jev-1.13 412ms"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q lacks %q", row, want)
		}
	}
	if strings.Contains(row, rolemanager.HarnessName) {
		t.Errorf("a Jev decision names the harness: %q", row)
	}
	blocked := plainRow(rmRowSegs(rmTestMsg("Checked the command", rolemanager.ToneBlocked, jevMeta, 0), 120))
	if !strings.Contains(blocked, "✗") || strings.Contains(blocked, "ms") {
		t.Errorf("blocked row = %q", blocked)
	}
	fell := plainRow(rmRowSegs(rmTestMsg("Checked the command", rolemanager.ToneCaution, RMMeta{Icon: "shield_fallback", Category: "security", Cause: "fallback", ActorKind: "model", Actor: "claude-haiku-4.5"}, 0), 120))
	if !strings.Contains(fell, "!") || !strings.Contains(fell, "↩") || !strings.Contains(fell, "claude-haiku-4.5") {
		t.Errorf("fallback row = %q", fell)
	}
	if got := plainRow(rmRowSegs(rmTestMsg("Cached", rolemanager.ToneClear, RMMeta{ActorKind: "harness", Actor: "belai", Category: "security", Icon: "recall", Cause: "cache_hit"}, 0), 120)); !strings.Contains(got, "belai") || !strings.Contains(got, "⟳") {
		t.Errorf("harness row = %q", got)
	}
}

func TestRMRowDropsTheTagBeforeTheOutcome(t *testing.T) {
	row := plainRow(rmRowSegs(rmTestMsg("Checked the command", rolemanager.ToneClear, jevMeta, 412), 36))
	if strings.Contains(row, "jev-1.13") || !strings.Contains(row, "cleared it") {
		t.Errorf("narrow row = %q", row)
	}
}

func TestEveryRMGlyphIsOneCell(t *testing.T) {
	for id, g := range rmGlyphs {
		if lipgloss.Width(g) != 1 {
			t.Errorf("glyph %q (%s) is %d cells", g, id, lipgloss.Width(g))
		}
	}
	for _, c := range rolemanager.Categories {
		if rmASCII[string(c)] == "" {
			t.Errorf("category %s has no ASCII fallback", c)
		}
	}
	if got := RMGlyph("no-such-icon", "tools"); got != "T" {
		t.Errorf("unknown icon = %q, want the category letter", got)
	}
}

func TestEveryEventIconHasAGlyph(t *testing.T) {
	for _, id := range rolemanager.Icons() {
		if _, ok := rmGlyphs[id]; !ok {
			t.Errorf("icon %q has no glyph", id)
		}
	}
}

// TestTUIDesignListsRMGlyphsAndColours keeps docs/tui-design.md in step with
// the glyph table and the category colours.
func TestTUIDesignListsRMGlyphsAndColours(t *testing.T) {
	doc := docparity.Read(t, "docs/tui-design.md")
	for id, g := range rmGlyphs {
		if !strings.Contains(doc, g) {
			t.Errorf("docs/tui-design.md does not list glyph %q (%s)", g, id)
		}
	}
	for _, g := range []string{"✓", "!", "✗", "↩", "⟳", "◔"} {
		if !strings.Contains(doc, g) {
			t.Errorf("docs/tui-design.md does not list marker %q", g)
		}
	}
	for _, tok := range []string{"ColorCatSecurity", "ColorCatMode", "ColorCatContext", "ColorCatTools", "ColorCatCode", "ColorCatAsk", "ColorCatRouting"} {
		if !strings.Contains(doc, tok) {
			t.Errorf("docs/tui-design.md does not list %s", tok)
		}
	}
	for _, c := range rolemanager.Categories {
		if !strings.Contains(doc, "`"+string(c)+"`") {
			t.Errorf("docs/tui-design.md does not name the category %q", c)
		}
	}
}
