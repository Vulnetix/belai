package components

import (
	"github.com/vulnetix/belai/internal/docparity"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/vulnetix/belai/internal/agentprofile"
)

// withTrueColor renders in truecolour for the test, so the style assertions
// have ANSI to look at, and puts the brand palette and the profile back after.
func withTrueColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(prev)
		ResetPersona()
	})
}

// kremvax is belai:patcher's palette (internal/agentprofile/builtin/patcher.json).
var kremvax = []string{"#b3262e", "#46566b", "#c86369", "#7a8594"}

func TestApplyPersonaRetintsTheAccents(t *testing.T) {
	withTrueColor(t)
	brandTeal, brandSoft := ColorTeal, ColorTealSoft
	gen := ThemeGeneration()

	if !ApplyPersona(kremvax) {
		t.Fatal("a four-colour palette was refused")
	}
	if ColorTeal == brandTeal || ColorTealSoft == brandSoft {
		t.Fatal("the accents did not change")
	}
	if ThemeGeneration() == gen {
		t.Fatal("the theme generation did not move")
	}
	// The package-level styles copied the accents when they were declared, so
	// they have to be rebuilt: a render must carry the persona's own colour.
	if got := AccentStyle.Render("x"); !strings.Contains(got, "179;38;46") && !strings.Contains(got, "b3262e") {
		// Truecolour SGR carries the decimal channels of the colour in use. On
		// a dark terminal the primary is lightened, so read it back from the
		// colour the style was built with instead.
		want := lipgloss.NewStyle().Foreground(ColorTeal).Render("x")
		if got != want {
			t.Fatalf("AccentStyle was not rebuilt: %q, want %q", got, want)
		}
	}
	if KeyStyle.Render("k") != lipgloss.NewStyle().Foreground(ColorTealSoft).Bold(true).Render("k") {
		t.Fatal("KeyStyle was not rebuilt from the secondary")
	}
	if Chip("agent", ColorTeal) != chipStyle.Background(ColorTeal).Render("agent") {
		t.Fatal("chipStyle was not rebuilt")
	}
}

func TestPersonaLeavesTheSafetyAndStructureColoursAlone(t *testing.T) {
	withTrueColor(t)
	amber, danger, cream, muted, text, line := ColorAmber, ColorDanger, ColorCream, ColorMuted, ColorText, ColorLine

	ApplyPersona(kremvax)

	if ColorAmber != amber || ColorDanger != danger {
		t.Fatal("a persona moved the colours that carry safety meaning")
	}
	if ColorCream != cream || ColorMuted != muted || ColorText != text || ColorLine != line {
		t.Fatal("a persona moved the text and structure colours")
	}
}

func TestResetPersonaRestoresTheBrand(t *testing.T) {
	withTrueColor(t)
	brandTeal, brandSoft := ColorTeal, ColorTealSoft
	brandAccent := AccentStyle.Render("x")

	ApplyPersona(kremvax)
	ResetPersona()

	if ColorTeal != brandTeal || ColorTealSoft != brandSoft || AccentStyle.Render("x") != brandAccent {
		t.Fatal("the brand accents did not come back")
	}
	gen := ThemeGeneration()
	ResetPersona()
	if ThemeGeneration() != gen {
		t.Fatal("resetting a palette that is not applied must not invalidate the cache")
	}
}

func TestAPaletteThatIsNotFourColoursTakesThePersonaOff(t *testing.T) {
	withTrueColor(t)
	brand := ColorTeal
	for name, bad := range map[string][]string{
		"none":          nil,
		"three":         {"#b3262e", "#46566b", "#c86369"},
		"five":          {"#b3262e", "#46566b", "#c86369", "#7a8594", "#000000"},
		"not hex":       {"red", "#46566b", "#c86369", "#7a8594"},
		"short hex":     {"#b32", "#46566b", "#c86369", "#7a8594"},
		"missing hash":  {"b3262e", "#46566b", "#c86369", "#7a8594"},
		"control bytes": {"#b3262e\x1b", "#46566b", "#c86369", "#7a8594"},
	} {
		ApplyPersona(kremvax)
		if ApplyPersona(bad) {
			t.Errorf("%s: a bad palette was accepted", name)
		}
		if ColorTeal != brand {
			t.Errorf("%s: a bad palette left the previous persona on", name)
		}
	}
}

// A palette colour reads at 4.5:1 on a typical terminal of its theme, and the
// text-on-fill chip stays readable too, for every built-in persona.
func TestEveryBuiltInPersonaReadsOnBothThemes(t *testing.T) {
	withTrueColor(t)
	list, err := agentprofile.List()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, p := range list {
		if len(p.Palette) == 0 {
			continue
		}
		seen++
		if !PersonaPalette(p.Palette) {
			t.Errorf("%s: palette %v is not four #rrggbb colours", p.Name, p.Palette)

			continue
		}
		ApplyPersona(p.Palette)
		for role, c := range map[string]lipgloss.AdaptiveColor{"primary": ColorTeal, "secondary": ColorTealSoft} {
			if r := contrast(c.Dark, personaDarkBG); r < personaMinText {
				t.Errorf("%s %s dark %s is %.2f:1 on %s", p.Name, role, c.Dark, r, personaDarkBG)
			}
			if r := contrast(c.Light, personaLightBG); r < personaMinText {
				t.Errorf("%s %s light %s is %.2f:1 on %s", p.Name, role, c.Light, r, personaLightBG)
			}
		}
		// The mode chip is ColorInk on a ColorTeal fill (bold, so 3:1 is the
		// floor for large text; the default palette clears it too).
		if r := contrast(ColorTeal.Dark, ColorInk.Dark); r < 3 {
			t.Errorf("%s chip dark: %s on %s is %.2f:1", p.Name, ColorInk.Dark, ColorTeal.Dark, r)
		}
		if r := contrast(ColorTeal.Light, ColorInk.Light); r < 3 {
			t.Errorf("%s chip light: %s on %s is %.2f:1", p.Name, ColorInk.Light, ColorTeal.Light, r)
		}
	}
	if seen < 10 {
		t.Fatalf("only %d built-in personas carry a palette, want the ten", seen)
	}
}

func TestFitKeepsAColourThatAlreadyReadsAndMovesOneThatDoesNot(t *testing.T) {
	// Already readable on both: kept exactly.
	if got := fitOn("#0e7c9b", personaLightBG, "#000000"); got != "#0e7c9b" {
		t.Fatalf("a readable colour moved to %s", got)
	}
	// Too dark for a dark terminal: lightened toward white, hue kept (still red-dominant).
	got := fitOn("#b3262e", personaDarkBG, "#FFFFFF")
	if got == "#b3262e" || contrast(got, personaDarkBG) < personaMinText {
		t.Fatalf("lightened to %s (%.2f:1)", got, contrast(got, personaDarkBG))
	}
	r, g, b := parseHex(got)
	if r <= g || r <= b {
		t.Fatalf("%s lost the red hue", got)
	}
	// Too light for a light terminal: darkened toward black.
	got = fitOn("#00e0c9", personaLightBG, "#000000")
	if contrast(got, personaLightBG) < personaMinText {
		t.Fatalf("darkened to %s (%.2f:1)", got, contrast(got, personaLightBG))
	}
	// Mid-grey on a mid-grey ground is impossible to read: it ends at the pole, never loops.
	if got := fitOn("#777777", "#777777", "#FFFFFF"); got == "" {
		t.Fatal("no colour returned")
	}
}

func TestHexRoundTrip(t *testing.T) {
	for _, h := range []string{"#000000", "#ffffff", "#b3262e", "#0e7c9b", "#010203"} {
		r, g, b := parseHex(h)
		if formatHex(r, g, b) != h {
			t.Errorf("%s round-tripped to %s", h, formatHex(r, g, b))
		}
	}
}

// A message is cached as ANSI with the colours baked in, so a persona change
// has to miss the cache: the transcript on screen re-themes, and taking the
// persona off brings the brand colours back.
func TestAPersonaChangeRethemesTheRenderedTranscript(t *testing.T) {
	withTrueColor(t)
	ml := mixedTranscript()

	brand, _ := ml.Render()
	if again, _ := ml.Render(); again != brand {
		t.Fatal("setup: two renders under one palette differ")
	}

	ApplyPersona(kremvax)
	themed, _ := ml.Render()
	if themed == brand {
		t.Fatal("the cached rows kept the brand colours after the persona changed")
	}

	ResetPersona()
	if back, _ := ml.Render(); back != brand {
		t.Fatal("taking the persona off did not bring the brand colours back")
	}
}

// TestTUIDesignAgentPersonasMatchTheCode keeps the Agent personas section equal
// to the code it describes: the palette size, the contrast it promises, the
// names of the interface and the roles it says never move.
func TestTUIDesignAgentPersonasMatchTheCode(t *testing.T) {
	doc := docparity.Read(t, "docs/tui-design.md")
	m := regexp.MustCompile(`(?s)### Agent personas(.*?)\n## `).FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("the Agent personas section moved")
	}
	section := strings.Join(strings.Fields(m[1]), " ")

	if agentprofile.PaletteSize != 4 || !strings.Contains(section, "palette of four colours") || !strings.Contains(section, "Exactly four `#rrggbb` colours (`PaletteSize`)") {
		t.Errorf("the page and agentprofile.PaletteSize (%d) disagree on the palette size", agentprofile.PaletteSize)
	}
	if want := strconv.FormatFloat(personaMinText, 'f', 1, 64) + ":1"; !strings.Contains(section, "reads at "+want) {
		t.Errorf("the page does not promise %s for a palette colour", want)
	}
	if !strings.Contains(section, "3:1 for the chip text") {
		t.Error("the page does not give the 3:1 floor for the chip text that the test holds")
	}
	for _, name := range []string{"ApplyPersona", "ResetPersona", "ThemeGeneration", "syncPersona", "refreshFooter", "PaletteSize"} {
		if !strings.Contains(section, name) {
			t.Errorf("the page does not name %s", name)
		}
	}
	// The roles the page says are fixed are exactly the ones a persona leaves alone.
	for _, role := range []string{"ColorAmber", "ColorDanger", "ColorCream", "ColorMuted", "ColorText", "ColorLine"} {
		if !strings.Contains(section, "`"+role+"`") {
			t.Errorf("the page does not list %s as never overridden", role)
		}
	}
	// The two roles it says a persona replaces are the two ApplyPersona changes.
	withTrueColor(t)
	before := map[string]lipgloss.AdaptiveColor{"ColorTeal": ColorTeal, "ColorTealSoft": ColorTealSoft}
	ApplyPersona(kremvax)
	after := map[string]lipgloss.AdaptiveColor{"ColorTeal": ColorTeal, "ColorTealSoft": ColorTealSoft}
	for role := range before {
		if before[role] == after[role] {
			t.Errorf("%s did not change although the page says the persona replaces it", role)
		}
		if !strings.Contains(section, "`"+role+"`") {
			t.Errorf("the page does not name %s as replaced", role)
		}
	}
}
