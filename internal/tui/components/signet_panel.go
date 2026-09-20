package components

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// signetPanel renders a coalesced run of adjacent system notices as one framed
// panel titled signet: one body line per notice with a muted · gutter, bodies
// muted, the frame's edges in the line colour and the title in the brand
// accent. It truncates like any other panel via signetPreviewLines and returns
// a per-body-line owner index so provenance can point each line back to the
// notice that produced it.
func signetPanel(msgs []Message, idxs []int, width int, expandAll bool) (string, LineMap, []int) {
	inner := max(width-4, 8)
	icol := visibleLen("· ")

	var rows []Row
	var owners []int
	for _, idx := range idxs {
		text := strings.TrimRight(msgs[idx].Text(), "\n")
		phys := strings.Split(text, "\n")
		if len(phys) == 0 {
			phys = []string{""}
		}
		first := true
		for _, pline := range phys {
			pline = strings.ReplaceAll(pline, "\t", " ")
			lines := wrapTextLines(pline, max(inner-icol, 1))
			if len(lines) == 0 {
				lines = []string{""}
			}
			for j, line := range lines {
				r := Row{Gutter: icol}
				if first && j == 0 {
					r.Segs = []Seg{NewSeg("· ", ColorMuted), NewSeg(line, ColorMuted)}
				} else {
					r.Segs = []Seg{NewSeg(spaces(icol), nil), NewSeg(line, ColorMuted)}
				}
				rows = append(rows, r)
				owners = append(owners, idx)
			}
			first = false
		}
	}

	if !expandAll && len(rows) > signetPreviewLines {
		hidden := signetHidden(msgs, owners, signetPreviewLines)
		marker := "… " + strconv.Itoa(len(rows)-signetPreviewLines) + " more lines"
		firstHidden := owners[signetPreviewLines]
		rows = append(rows[:signetPreviewLines], Row{
			Segs:        []Seg{NewSeg(marker, ColorMuted)},
			MarkerCol:   0,
			MarkerWidth: visibleLen(marker),
			Hidden:      hidden,
		})
		owners = append(owners[:signetPreviewLines], firstHidden)
	}

	p := Panel{
		Title:       "signet",
		TitleAccent: lipgloss.TerminalColor(ColorTeal),
		Accent:      lipgloss.TerminalColor(ColorLine),
		Width:       width,
		BodyRows:    rows,
	}
	s, lm := p.Render()
	return s, lm, owners
}

// signetHidden joins the raw text of the notices whose rows were hidden,
// deduplicating so a notice that still has visible rows is not duplicated in a
// selection over the hint.
func signetHidden(msgs []Message, owners []int, from int) string {
	var parts []string
	seen := map[int]bool{}
	for _, o := range owners[from:] {
		if !seen[o] {
			seen[o] = true
			parts = append(parts, strings.TrimRight(msgs[o].Text(), "\n"))
		}
	}
	return strings.Join(parts, "\n")
}

// tagGroupProvenance stamps a coalesced system group's lines: per-line owner
// from the owners slice (consumed in order for each non-chrome body line), and
// group-level Copyable (any member has text) and Collapsed (from the group's
// own truncation). File stays false — notices are never file panels.
func tagGroupProvenance(lm LineMap, owners []int, msgs []Message) {
	copyable := false
	for _, o := range owners {
		if o >= 0 && o < len(msgs) && strings.TrimSpace(msgs[o].Text()) != "" {
			copyable = true
		}
	}
	collapsed := hasTruncation(lm)
	j := 0
	for i := range lm {
		if lm[i].Chrome {
			lm[i].Owner = -1
			continue
		}
		if j < len(owners) {
			lm[i].Owner = owners[j]
			j++
		} else {
			lm[i].Owner = -1
		}
		lm[i].Copyable = copyable
		lm[i].Collapsed = collapsed
	}
}
