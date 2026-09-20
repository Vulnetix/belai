package components

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// signetPanel renders a coalesced group of system notices as one signet panel.
// Each notice becomes one body line. Long groups are truncated to
// signetPreviewLines with a marker whose selection copies the hidden notices.
func signetPanel(messages []Message, idxs []int, width int, expandAll bool) (string, LineMap, []int) {
	var parts []string
	for _, idx := range idxs {
		text := strings.TrimRight(messages[idx].Text(), "\n")
		parts = append(parts, text)
	}
	body := strings.Join(parts, "\n")
	marker, hidden := "", ""
	if !expandAll {
		var m string
		body, m, hidden = truncateSignetBody(body, signetPreviewLines)
		marker = m
	}

	s, lm := Panel{
		Title:  "signet",
		Body:   MutedStyle.Render(body),
		Width:  width,
		Accent: lipgloss.TerminalColor(ColorMuted),
		Marker: marker,
		Hidden: hidden,
	}.Render()

	owners := make([]int, len(lm))
	for i := range lm {
		if lm[i].Chrome {
			owners[i] = -1
			continue
		}
		if marker != "" && lm[i].MarkerWidth > 0 {
			owners[i] = -1
			continue
		}
		if len(idxs) > 0 {
			owners[i] = idxs[0]
		}
	}
	return s, lm, owners
}

// truncateSignetBody keeps the first maxLines raw lines and returns a muted
// marker plus the hidden remainder.
func truncateSignetBody(body string, maxLines int) (out, marker, hidden string) {
	lines := strings.Split(body, "\n")
	keep := maxLines
	if keep < 1 {
		keep = 1
	}
	if len(lines) <= keep {
		return body, "", ""
	}
	kept := strings.Join(lines[:keep], "\n")
	hidden = strings.Join(lines[keep:], "\n")
	marker = "… " + strconv.Itoa(len(lines)-keep) + " more lines"
	return kept + "\n" + MutedStyle.Render(marker), marker, hidden
}

// tagGroupProvenance marks the line map of a coalesced system group so each
// line carries the message that owns it, when signetPanel produced a 1:1
// owners slice. Chrome and marker lines keep the sentinel owner.
func tagGroupProvenance(lm LineMap, owners []int, messages []Message) {
	for i := range lm {
		if i < len(owners) {
			lm[i].Owner = owners[i]
		}
		if lm[i].Chrome {
			lm[i].Owner = -1
			continue
		}
		owner := lm[i].Owner
		if owner >= 0 && owner < len(messages) {
			msg := messages[owner]
			lm[i].File = msg.FilePath() != ""
			lm[i].Copyable = strings.TrimSpace(msg.Text()) != ""
		}
	}
}
