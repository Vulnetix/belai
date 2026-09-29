package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/rolemanager"
)

// A role-manager row is one line: an icon that names the activity, coloured
// by its category; the summary; the outcome with a marker that does not rely
// on hue; and the decider with how long it took. docs/tui-design.md lists
// every glyph and colour used here.

// RMMeta is the presentation metadata of a "rolemanager" row. Every field is
// a harness-derived enum or short name; none is model text.
type RMMeta struct {
	ActorKind string // jev, model, harness
	Actor     string // short model id, or belai
	Category  string
	Icon      string // stable icon id
	Cause     string // fallback, cache_hit, timeout, none
	Outcome   string // clear, caution, blocked, neutral
	Score     int    // percent; meaningful only when HasScore
	HasScore  bool
}

// rmGlyphs maps an icon id to its glyph. A glyph that a terminal draws at any
// width but one cell falls back to its category's ASCII letter, so columns
// stay aligned.
var rmGlyphs = map[string]string{
	"shield": "◈", "shield_unclear": "◇", "phase": "▸", "shield_fallback": "◊",
	"recall": "↺", "recall_bad": "↯", "seal": "⊕", "seal_broken": "⊘", "mismatch": "≠",
	"mode": "◐", "intent": "◎", "pin": "⊙", "limit": "⊤", "goal": "◉", "goal_repair": "↻",
	"plan": "≡", "agent": "◍", "draft": "✎",
	"compact": "⊟", "prune": "⊠", "page": "▤", "deps": "⊡",
	"swap": "⇄", "replan": "⇆", "search": "⊛", "select": "⊞",
	"server": "⌘", "diagnose": "⌥", "server_down": "⊗", "triage": "▣",
	"clarify": "?", "options": "≣",
	"route": "⇢", "pool": "◫",
	"name": "✦", "dot": "·",
}

// rmASCII is the fallback letter for each category.
var rmASCII = map[string]string{
	"security": "S", "mode": "M", "context": "C", "tools": "T",
	"code": "L", "ask": "?", "routing": "R", "housekeeping": ".",
}

// RMGlyphIDs lists the icon ids that have a glyph, for the parity test.
func RMGlyphIDs() []string {
	out := make([]string, 0, len(rmGlyphs))
	for id := range rmGlyphs {
		out = append(out, id)
	}
	return out
}

// RMGlyph returns the glyph for an icon id, or its category's ASCII letter
// when the glyph is unknown or not one cell wide.
func RMGlyph(icon, category string) string {
	if g, ok := rmGlyphs[icon]; ok && lipgloss.Width(g) == 1 {
		return g
	}
	if a, ok := rmASCII[category]; ok {
		return a
	}
	return "·"
}

// rmCategoryColor is the icon colour of a category.
func rmCategoryColor(category string) lipgloss.TerminalColor {
	switch rolemanager.Category(category) {
	case rolemanager.CategorySecurity:
		return ColorCatSecurity
	case rolemanager.CategoryMode:
		return ColorCatMode
	case rolemanager.CategoryContext:
		return ColorCatContext
	case rolemanager.CategoryTools:
		return ColorCatTools
	case rolemanager.CategoryCode:
		return ColorCatCode
	case rolemanager.CategoryAsk:
		return ColorCatAsk
	case rolemanager.CategoryRouting:
		return ColorCatRouting
	}
	return ColorLow
}

// rmOutcomeMarker is the mark beside the outcome word, so the result reads
// without colour.
func rmOutcomeMarker(t rolemanager.Tone) string {
	switch t {
	case rolemanager.ToneClear:
		return "✓"
	case rolemanager.ToneCaution:
		return "!"
	case rolemanager.ToneBlocked:
		return "✗"
	}
	return ""
}

// rmCauseMark is the mark that says a decision did not take the plain path.
func rmCauseMark(cause string) string {
	glyph, ascii := "", ""
	switch rolemanager.Cause(cause) {
	case rolemanager.CauseFallback:
		glyph, ascii = "↩", "<"
	case rolemanager.CauseCacheHit:
		glyph, ascii = "⟳", "="
	case rolemanager.CauseTimeout:
		glyph, ascii = "◔", "~"
	default:
		return ""
	}
	if lipgloss.Width(glyph) != 1 {
		return ascii
	}
	return glyph
}

// rmActorTag is the right-hand tag: who decided, and how long it took.
func rmActorTag(meta RMMeta, ms int64) (string, lipgloss.TerminalColor) {
	name := meta.Actor
	if name == "" {
		return "", ColorLow
	}
	if ms > 0 {
		name += " " + rmDuration(ms)
	}
	switch rolemanager.ActorKind(meta.ActorKind) {
	case rolemanager.ActorJev:
		return name, ColorTealSoft
	case rolemanager.ActorModel:
		return name, ColorMuted
	}
	return name, ColorLow
}

func rmDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// rmRowSegs builds the single line of a role-manager row for a width in cells.
// The summary is what gives way when the line is too long: the icon, outcome
// and decider are kept. With no room for the decider tag, it is dropped before
// the outcome is cut.
func rmRowSegs(msg Message, width int) []Seg {
	meta := msg.RMMeta
	icon := NewSeg(RMGlyph(meta.Icon, meta.Category)+" ", rmCategoryColor(meta.Category))

	outcome := msg.RM.Outcome
	if m := rmOutcomeMarker(msg.RM.Tone); m != "" {
		outcome += " " + m
	}
	if c := rmCauseMark(meta.Cause); c != "" {
		outcome += " " + c
	}
	tag, tagColor := rmActorTag(meta, msg.DurationMS)

	build := func(summary string, withTag bool) []Seg {
		segs := []Seg{icon, NewSeg(summary, nil), NewSeg(" — ", ColorLow), NewSeg(outcome, toneColor(msg.RM.Tone))}
		if withTag && tag != "" {
			segs = append(segs, NewSeg(" · ", ColorLow), NewSeg(tag, tagColor))
		}
		return segs
	}
	fixed := func(withTag bool) int {
		n := 2 + lipgloss.Width(" — ") + lipgloss.Width(outcome)
		if withTag && tag != "" {
			n += lipgloss.Width(" · ") + lipgloss.Width(tag)
		}
		return n
	}
	const minSummary = 8
	summary := msg.RM.Summary
	for _, withTag := range []bool{true, false} {
		room := width - fixed(withTag)
		if room >= minSummary || !withTag {
			if lipgloss.Width(summary) > room {
				summary = truncateCells(summary, max(room, 1))
			}
			segs, _ := clipSegs(build(summary, withTag), width)
			return segs
		}
	}
	return nil
}

// truncateCells cuts s to at most n cells, ending in an ellipsis.
func truncateCells(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > n-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}
