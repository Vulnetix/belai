package components

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// signetPanel renders a coalesced run of adjacent system notices and tool
// results as one framed panel titled signet. System notices render as muted
// · lines; tool results keep their existing colour, status and body styling
// but are nested inside the panel instead of rendered as flat rows. The
// panel truncates long runs of system notices via signetPreviewLines, but
// groups that contain any tool result render in full because tool rows
// already truncate their own content.
func signetPanel(msgs []Message, idxs []int, width int, expandAll bool) (string, LineMap, []int) {
	first := &msgs[idxs[0]]
	systemOnly := true
	for _, idx := range idxs {
		if msgs[idx].Role != "system" {
			systemOnly = false
			break
		}
	}

	key := renderKeyFor(first, width, expandAll)
	key.groupN = len(idxs)
	for _, idx := range idxs {
		key.groupLen += len(msgs[idx].Text())
	}
	if systemOnly && !key.started && first.rc.key == key {
		return first.rc.text, first.rc.lm, first.rc.owners
	}

	s, lm, owners := renderSignetPanel(msgs, idxs, width, expandAll)
	if systemOnly && !key.started {
		first.rc = renderCache{key: key, text: s, lm: lm, owners: owners}
	}
	return s, lm, owners
}

// renderSignetPanel builds the framed signet panel for a group of system
// notices and tool results, including the panel borders and a fully
// provenanced LineMap.
func renderSignetPanel(msgs []Message, idxs []int, width int, expandAll bool) (string, LineMap, []int) {
	width = max(width, panelMinWidth)
	inner := max(width-4, 8)
	barCol := visibleLen("│ ") // border plus padding
	icol := visibleLen("· ")

	title := "signet"
	leftPlain := "╭─ " + title + " "
	rightPlain := "─╮"
	fill := width - visibleLen(leftPlain) - visibleLen(rightPlain)
	if fill < 0 {
		// Drop the closing decoration before truncating the title.
		rightPlain = "─╮"
		fill = width - visibleLen(leftPlain) - visibleLen(rightPlain)
	}
	if fill < 0 {
		title = truncateRunes(title, len([]rune(title))+fill)
		leftPlain = "╭─ " + title + " "
		fill = max(width-visibleLen(leftPlain)-visibleLen(rightPlain), 0)
	}

	edge := lipgloss.NewStyle().Foreground(ColorLine)
	titleStyle := lipgloss.NewStyle().Foreground(ColorTeal).Bold(true)
	top := edge.Render("╭─ ") + titleStyle.Render(title) + edge.Render(" "+repeatRune('─', fill)) + edge.Render(rightPlain)
	bottom := edge.Render("╰" + repeatRune('─', width-2) + "╯")
	bar := edge.Render("│")

	var bodyLines []string
	var bodyLm LineMap
	var lineOwners []int
	var isSystemLine []bool
	groupCopyable := false
	hasTool := false

	// First pass computes copyability from any member with text.
	for _, idx := range idxs {
		if msgs[idx].Role == "tool" {
			hasTool = true
		}
		if strings.TrimSpace(msgs[idx].Text()) != "" {
			groupCopyable = true
		}
	}

	// Second pass renders each member inside the panel.
	for _, idx := range idxs {
		msg := msgs[idx]
		switch msg.Role {
		case "system":
			lineOwners, isSystemLine, bodyLines, bodyLm = renderSignetSystemLines(msg, idx, inner, bar, barCol, icol, groupCopyable, lineOwners, isSystemLine, bodyLines, bodyLm)
		case "tool":
			lineOwners, isSystemLine, bodyLines, bodyLm = renderSignetToolLines(msg, idx, inner, bar, barCol, expandAll, groupCopyable, lineOwners, isSystemLine, bodyLines, bodyLm)
		}
	}

	// Truncate system-only groups line-by-line, preserving the existing signet
	// preview behaviour. Groups containing tools render in full because each
	// tool row already applies its own preview/truncation and splitting a tool
	// result mid-message would hide its status and body.
	panelCollapsed := false
	if !expandAll && !hasTool && len(bodyLines) > signetPreviewLines {
		hidden := signetHidden(msgs, lineOwners, signetPreviewLines)
		marker := "… " + strconv.Itoa(len(bodyLines)-signetPreviewLines) + " more lines"
		firstHidden := lineOwners[signetPreviewLines]

		markerPlain := marker
		pad := inner - visibleLen(markerPlain)
		if pad < 0 {
			pad = 0
		}
		markerLine := bar + " " + markerPlain + spaces(pad) + " " + bar

		bodyLines = append(bodyLines[:signetPreviewLines], markerLine)
		bodyLm = append(bodyLm[:signetPreviewLines], SourceLine{
			Text:        markerPlain,
			Col:         barCol,
			Width:       visibleLen(markerPlain),
			MarkerCol:   barCol,
			MarkerWidth: visibleLen(markerPlain),
			Hidden:      hidden,
			Owner:       firstHidden,
			Copyable:    groupCopyable,
			Collapsed:   true,
		})
		lineOwners = append(lineOwners[:signetPreviewLines], firstHidden)
		isSystemLine = append(isSystemLine[:signetPreviewLines], true)
		panelCollapsed = true
	}

	// Apply group-level collapsed state to system lines. Tool lines keep their
	// own collapsed state as set by tagProvenance.
	for i := range bodyLm {
		bodyLm[i].Copyable = groupCopyable
		if isSystemLine[i] {
			bodyLm[i].Collapsed = panelCollapsed
		}
	}

	var b strings.Builder
	var lm LineMap
	b.WriteString(top + "\n")
	lm = append(lm, SourceLine{Chrome: true, Owner: -1})
	for _, line := range bodyLines {
		b.WriteString(line + "\n")
	}
	b.WriteString(bottom)
	lm = append(lm, bodyLm...)
	lm = append(lm, SourceLine{Chrome: true, Owner: -1})

	return b.String(), lm, lineOwners
}

// renderSignetSystemLines adds a system notice's wrapped, indented lines to
// the panel body and returns updated owner, type and line slices.
func renderSignetSystemLines(msg Message, owner, inner int, bar string, barCol, icol int, groupCopyable bool, owners []int, isSystem []bool, bodyLines []string, lm LineMap) ([]int, []bool, []string, LineMap) {
	text := strings.TrimRight(msg.Text(), "\n")
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
			prefix := "· "
			if !first || j > 0 {
				prefix = spaces(icol)
			}
			fullLine := prefix + line
			pad := inner - visibleLen(fullLine)
			if pad < 0 {
				pad = 0
			}
			rendered := bar + " " + fullLine + spaces(pad) + " " + bar
			bodyLines = append(bodyLines, rendered)

			col := barCol + visibleLen(prefix)
			owners = append(owners, owner)
			isSystem = append(isSystem, true)
			lm = append(lm, SourceLine{
				Text:     line,
				Col:      col,
				Width:    visibleLen(line),
				Owner:    owner,
				Copyable: groupCopyable,
			})
		}
		first = false
	}
	return owners, isSystem, bodyLines, lm
}

// renderSignetToolLines adds a tool result's existing row rendering to the
// panel body and returns updated owner, type and line slices. The tool row is
// rendered at the panel's inner width so that status alignment and content
// wrapping fit exactly between the borders.
func renderSignetToolLines(msg Message, owner, inner int, bar string, barCol int, expandAll bool, groupCopyable bool, owners []int, isSystem []bool, bodyLines []string, lm LineMap) ([]int, []bool, []string, LineMap) {
	toolStr, toolLm := toolRow(msg, inner, expandAll)
	tagProvenance(toolLm, owner, msg)
	toolLines := strings.Split(toolStr, "\n")
	for j, line := range toolLines {
		pad := inner - visibleLen(line)
		if pad < 0 {
			pad = 0
		}
		rendered := bar + " " + line + spaces(pad) + " " + bar
		bodyLines = append(bodyLines, rendered)

		var sl SourceLine
		if j < len(toolLm) {
			sl = toolLm[j]
			sl.Col += barCol
			if sl.MarkerWidth > 0 {
				sl.MarkerCol += barCol
			}
		}
		sl.Owner = owner
		sl.Copyable = groupCopyable
		owners = append(owners, owner)
		isSystem = append(isSystem, false)
		lm = append(lm, sl)
	}
	return owners, isSystem, bodyLines, lm
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
