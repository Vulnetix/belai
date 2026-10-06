package components

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// roles maps the colour role names docs/tui-design.md uses to the palette.
var roles = map[string]lipgloss.AdaptiveColor{
	"ColorLine": ColorLine, "ColorLow": ColorLow, "ColorMuted": ColorMuted, "ColorText": ColorText,
	"ColorCream": ColorCream, "ColorTeal": ColorTeal, "ColorTealSoft": ColorTealSoft, "ColorYou": ColorYou,
	"ColorVoice": ColorVoice, "ColorAmber": ColorAmber, "ColorDanger": ColorDanger,
	"ColorDiffAddBg": ColorDiffAddBg, "ColorDiffDelBg": ColorDiffDelBg,
}

// TestTUIDesignColourTableMatchesThePalette keeps every hex value in the Colour
// roles table equal to theme.go, for both themes, and the table complete.
func TestTUIDesignColourTableMatchesThePalette(t *testing.T) {
	doc := docparity.Read(t, "docs/tui-design.md")
	section := regexp.MustCompile(`(?s)## Colour roles(.*?)## Glyphs`).FindStringSubmatch(doc)
	if section == nil {
		t.Fatal("the Colour roles section moved")
	}
	seen := map[string]bool{}
	row := regexp.MustCompile("(?m)^\\| `(Color[A-Za-z]+)` \\| `(#[0-9A-Fa-f]{6})` \\| `(#[0-9A-Fa-f]{6})` \\|")
	for _, m := range row.FindAllStringSubmatch(section[1], -1) {
		c, ok := roles[m[1]]
		if !ok {
			t.Errorf("the page documents %s, which the palette does not define", m[1])
			continue
		}
		seen[m[1]] = true
		if !strings.EqualFold(c.Dark, m[2]) || !strings.EqualFold(c.Light, m[3]) {
			t.Errorf("%s: the page says dark %s light %s, the code has dark %s light %s", m[1], m[2], m[3], c.Dark, c.Light)
		}
	}
	for name := range roles {
		if !seen[name] {
			t.Errorf("the Colour roles table has no row for %s", name)
		}
	}
	if !strings.Contains(section[1], "`ColorInk` (`"+ColorInk.Dark+"` dark, `"+ColorInk.Light+"` light)") {
		t.Error("the page does not give both ColorInk values")
	}
}

// luminance is the WCAG relative luminance of a #rrggbb colour.
func luminance(hex string) float64 {
	var ch [3]float64
	for i := 0; i < 3; i++ {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		f := float64(v) / 255
		if f <= 0.03928 {
			ch[i] = f / 12.92
		} else {
			ch[i] = math.Pow((f+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestTUIDesignContrastClaim checks "every role that renders text you must read
// (Muted and up) keeps 4.5:1 against the terminal background in its theme" for a
// typical dark terminal (#1E1E1E) and a white one, and that Line and Low are
// deliberately below it.
func TestTUIDesignContrastClaim(t *testing.T) {
	const darkBG, lightBG = "#1E1E1E", "#FFFFFF"
	readable := []string{"ColorMuted", "ColorText", "ColorCream", "ColorTeal", "ColorTealSoft", "ColorYou", "ColorVoice", "ColorAmber", "ColorDanger"}
	for _, name := range readable {
		c := roles[name]
		if r := contrast(c.Dark, darkBG); r < 4.5 {
			t.Errorf("%s dark %s is %.2f:1 on %s, the page promises 4.5:1", name, c.Dark, r, darkBG)
		}
		if r := contrast(c.Light, lightBG); r < 4.5 {
			t.Errorf("%s light %s is %.2f:1 on %s, the page promises 4.5:1", name, c.Light, r, lightBG)
		}
	}
	for _, name := range []string{"ColorLine", "ColorLow"} {
		c := roles[name]
		if contrast(c.Dark, darkBG) >= 4.5 && contrast(c.Light, lightBG) >= 4.5 {
			t.Errorf("%s is meant to sit below 4.5:1 and carries no required information", name)
		}
	}
}

// TestTUIDesignCategoryColoursMatchThePalette keeps the category table equal to
// the colours the row renderer uses, and to the ASCII fallback letters.
func TestTUIDesignCategoryColoursMatchThePalette(t *testing.T) {
	doc := docparity.Read(t, "docs/tui-design.md")
	want := map[string]struct {
		token string
		c     lipgloss.AdaptiveColor
	}{
		"security": {"ColorCatSecurity", ColorCatSecurity}, "mode": {"ColorCatMode", ColorCatMode},
		"context": {"ColorCatContext", ColorCatContext}, "tools": {"ColorCatTools", ColorCatTools},
		"code": {"ColorCatCode", ColorCatCode}, "ask": {"ColorCatAsk", ColorCatAsk},
		"routing": {"ColorCatRouting", ColorCatRouting}, "housekeeping": {"ColorLow", ColorLow},
	}
	for cat, w := range want {
		row := regexp.MustCompile("(?m)^\\| `" + cat + "` \\| `" + w.token + "` \\| `(#[0-9A-Fa-f]{6})` \\| `(#[0-9A-Fa-f]{6})` \\|").FindStringSubmatch(doc)
		if row == nil {
			t.Errorf("the category table has no row `%s` | `%s`", cat, w.token)
			continue
		}
		if !strings.EqualFold(row[1], w.c.Dark) || !strings.EqualFold(row[2], w.c.Light) {
			t.Errorf("%s: the page says %s/%s, the code has %s/%s", cat, row[1], row[2], w.c.Dark, w.c.Light)
		}
		if got := rmCategoryColor(cat); got != lipgloss.TerminalColor(w.c) {
			t.Errorf("rmCategoryColor(%q) is not %s", cat, w.token)
		}
	}
	letters := regexp.MustCompile(`falls back to its category's letter \(([^)]+)\)`).FindStringSubmatch(strings.Join(strings.Fields(doc), " "))
	if letters == nil {
		t.Fatal("the fallback letter list moved")
	}
	var got []string
	for _, l := range strings.Split(letters[1], ", ") {
		got = append(got, l)
	}
	order := []string{"security", "mode", "context", "tools", "code", "ask", "routing", "housekeeping"}
	for i, cat := range order {
		if got[i] != rmASCII[cat] {
			t.Errorf("the fallback letter for %s is %q on the page and %q in the code", cat, got[i], rmASCII[cat])
		}
	}
}

// TestTUIDesignIconTableMatchesTheCode keeps the icon table equal to the
// glyphs and categories the role-manager rows use: every glyph has a row, the
// row's category is the one its events belong to, and no row is invented.
func TestTUIDesignIconTableMatchesTheCode(t *testing.T) {
	doc := docparity.Read(t, "docs/tui-design.md")
	section := regexp.MustCompile(`(?s)\| Icon \| Activity \| Category \|(.*?)## Rhythm`).FindStringSubmatch(doc)
	if section == nil {
		t.Fatal("the icon table moved")
	}
	type key struct{ glyph, category string }
	rows := map[key]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `([^`]+)` \\| ([^|]+) \\| `([a-z]+)` \\|").FindAllStringSubmatch(section[1], -1) {
		rows[key{m[1], m[3]}] = true
	}
	cats := rolemanager.IconCategories()
	used := map[key]bool{}
	for id, glyph := range rmGlyphs {
		for _, c := range cats[id] {
			k := key{glyph, string(c)}
			used[k] = true
			if !rows[k] {
				t.Errorf("the icon table has no row for %s (%s) in the %s category", glyph, id, c)
			}
		}
		if len(cats[id]) == 0 {
			t.Errorf("the icon %q has a glyph but no event uses it", id)
		}
		if len(cats[id]) > 1 {
			t.Errorf("the icon %q is used by events of several categories: %v", id, cats[id])
		}
	}
	for k := range rows {
		if !used[k] {
			t.Errorf("the icon table lists %s in the %s category, which no event uses", k.glyph, k.category)
		}
	}
}
