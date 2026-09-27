package kanban

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// CleanTitle makes a title safe to store and render: ANSI sequences, delimiter markup,
// control and bidi runes removed, one line, at most MaxTitleRunes.
func CleanTitle(s string) string {
	s = strings.Join(strings.Fields(sessionsync.CleanPrompt(ansi.Strip(s))), " ")
	if utf8.RuneCountInString(s) > MaxTitleRunes {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:MaxTitleRunes-1])) + "…"
	}
	return s
}

// CleanBody makes multi-line text safe to store and render, capped at max
// bytes on a rune boundary.
func CleanBody(s string, max int) string {
	s = sessionsync.CleanPrompt(ansi.Strip(s))
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut])
}
