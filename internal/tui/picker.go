package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/tui/components"
)

// pickerRows is the maximum number of candidate rows shown at once in the
// filterable pickers shared by the @ file chooser and the /add-dir chooser.
const pickerRows = 5

// filterCandidates returns the elements of all that contain filter as a
// case-insensitive substring.
func filterCandidates(all []string, filter string) []string {
	if filter == "" {
		out := make([]string, len(all))
		copy(out, all)
		return out
	}
	lower := strings.ToLower(filter)
	var out []string
	for _, p := range all {
		if strings.Contains(strings.ToLower(p), lower) {
			out = append(out, p)
		}
	}
	return out
}

// cyclePicker moves the selection through candidates, wrapping around the
// ends. It is a no-op when candidates is empty.
func cyclePicker(cands []string, selected *int, delta int) {
	n := len(cands)
	if n == 0 {
		return
	}
	if *selected < 0 {
		*selected = 0
	} else {
		*selected += delta
	}
	for *selected < 0 {
		*selected += n
	}
	for *selected >= n {
		*selected -= n
	}
}

// selectedCandidate returns the currently highlighted candidate, if any.
func selectedCandidate(cands []string, selected int) (string, bool) {
	if selected < 0 || selected >= len(cands) {
		return "", false
	}
	return cands[selected], true
}

// pickerCounter returns the "k/n" counter string used by picker panels.
func pickerCounter(selected, total int) string {
	if total <= 0 {
		return "0/0"
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	return strconv.Itoa(selected+1) + "/" + strconv.Itoa(total)
}

// renderPicker draws a scrolling, filtered picker panel. It returns the
// rendered panel and the updated scroll position. If candidates is empty it
// returns an empty string and the supplied scroll value unchanged.
func renderPicker(title, meta string, width int, cands []string, selected, scroll int, header []string, accent lipgloss.TerminalColor) (string, int) {
	n := len(cands)
	if n == 0 {
		return "", scroll
	}
	// Clamp only the window anchor, not the highlight: a negative selection
	// means nothing is highlighted yet, so the first row must not appear
	// selected until the user moves.
	cursor := selected
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= n {
		cursor = n - 1
	}
	start := windowStart(scroll, cursor, n, pickerRows)

	var lines []string
	lines = append(lines, header...)
	for i := start; i < start+pickerRows && i < n; i++ {
		sel := i == selected
		style := components.MutedStyle
		if sel {
			style = components.EmphStyle
		}
		lines = append(lines, components.Cursor(sel)+style.Render(cands[i]))
	}

	return components.Panel{
		Title:  title,
		Meta:   meta,
		Body:   strings.Join(lines, "\n"),
		Width:  width,
		Accent: accent,
	}.View(), start
}
