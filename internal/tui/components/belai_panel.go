package components

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// belaiPanel renders a coalesced run of adjacent system notices and tool
// results as one framed panel titled belai. System notices render as muted
// · lines; tool results keep their existing colour, status and body styling
// but are nested inside the panel instead of rendered as flat rows. The
// panel truncates long runs of system notices via belaiPreviewLines, but
// groups that contain any tool result render in full because tool rows
// already truncate their own content.
func belaiPanel(msgs []Message, idxs []int, width int, expandAll bool) (string, LineMap, []int) {
	first := &msgs[idxs[0]]
	systemOnly := true
	for _, idx := range idxs {
		if msgs[idx].Role != "system" && msgs[idx].Role != "rolemanager" {
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

	s, lm, owners := renderBelaiPanel(msgs, idxs, width, expandAll)
	if systemOnly && !key.started {
		first.rc = renderCache{key: key, text: s, lm: lm, owners: owners}
	}
	return s, lm, owners
}

// renderBelaiPanel builds the framed belai panel for a group of system
// notices and tool results, including the panel borders and a fully
// provenanced LineMap.
func renderBelaiPanel(msgs []Message, idxs []int, width int, expandAll bool) (string, LineMap, []int) {
	width = max(width, panelMinWidth)
	inner := max(width-4, 8)
	barCol := visibleLen("│ ") // border plus padding
	icol := visibleLen("· ")

	// The group is an open block (docs/tui-design.md): a low rule on top and
	// no side or bottom edges. The member renderers still lay rows out as
	// `bar + " " + line + pad + " " + bar`; a blank bar keeps the two-column
	// indent (and so every line-map column) and the padding is trimmed on
	// output.
	edge := LowStyle
	titleStyle := lipgloss.NewStyle().Foreground(ColorMuted).Bold(true)
	bar := " "

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
			lineOwners, isSystemLine, bodyLines, bodyLm = renderBelaiSystemLines(msg, idx, inner, bar, barCol, icol, groupCopyable, lineOwners, isSystemLine, bodyLines, bodyLm)
		case "rolemanager":
			lineOwners, isSystemLine, bodyLines, bodyLm = renderBelaiActivityLines(msg, idx, inner, bar, barCol, icol, expandAll, groupCopyable, lineOwners, isSystemLine, bodyLines, bodyLm)
		case "tool":
			lineOwners, isSystemLine, bodyLines, bodyLm = renderBelaiToolLines(msg, idx, inner, bar, barCol, expandAll, groupCopyable, lineOwners, isSystemLine, bodyLines, bodyLm)
		}
	}

	// Truncate system-only groups line-by-line, preserving the existing belai
	// preview behaviour. Groups containing tools render in full because each
	// tool row already applies its own preview/truncation and splitting a tool
	// result mid-message would hide its status and body.
	panelCollapsed := false
	if !expandAll && !hasTool && len(bodyLines) > belaiPreviewLines {
		hidden := belaiHidden(msgs, lineOwners, belaiPreviewLines)
		marker := "… " + strconv.Itoa(len(bodyLines)-belaiPreviewLines) + " more lines"
		firstHidden := lineOwners[belaiPreviewLines]

		markerPlain := marker
		pad := inner - visibleLen(markerPlain)
		if pad < 0 {
			pad = 0
		}
		markerLine := bar + " " + markerPlain + spaces(pad) + " " + bar

		bodyLines = append(bodyLines[:belaiPreviewLines], markerLine)
		bodyLm = append(bodyLm[:belaiPreviewLines], SourceLine{
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
		lineOwners = append(lineOwners[:belaiPreviewLines], firstHidden)
		isSystemLine = append(isSystemLine[:belaiPreviewLines], true)
		panelCollapsed = true
	}

	// Apply group-level collapsed state to system lines. Tool lines keep their
	// own collapsed state as set by tagProvenance. Any collapsed content
	// inside the panel — a truncated system-only group or a collapsed tool
	// row — is enough to advertise the expand binding on the title bar.
	hasCollapsedContent := panelCollapsed
	for i := range bodyLm {
		bodyLm[i].Copyable = groupCopyable
		if isSystemLine[i] {
			bodyLm[i].Collapsed = panelCollapsed
		}
		if bodyLm[i].Collapsed {
			hasCollapsedContent = true
		}
	}

	// Build the title bar. When the panel has collapsed content, the binding
	// that expands it is shown in the top-right metadata, mirroring the helper
	// text the ask/composer panel carries in its own frame.
	meta := ""
	if hasCollapsedContent {
		meta = "ctrl+o expand all"
	}
	top := TopEdge(width, "belai", belaiDetail(msgs, idxs), meta, edge, titleStyle, true)

	lines := make([]string, 0, len(bodyLines)+1)
	lines = append(lines, top)
	for _, line := range bodyLines {
		lines = append(lines, strings.TrimRight(line, " "))
	}
	lm := append(LineMap{{Chrome: true, Owner: -1}}, bodyLm...)

	return strings.Join(lines, "\n"), lm, lineOwners
}

// belaiDetail is the low note on the group's rule: how many tools ran and,
// when any failed, how many.
func belaiDetail(msgs []Message, idxs []int) string {
	tools, failed := 0, 0
	for _, idx := range idxs {
		m := msgs[idx]
		if m.Role != "tool" {
			continue
		}
		tools++
		if strings.HasPrefix(strings.TrimSpace(m.Status), "✗") ||
			(strings.TrimSpace(m.Status) == "" && toolResultIsError(m.ToolName, m.Text())) {
			failed++
		}
	}
	if tools == 0 {
		return ""
	}
	s := countNoun(tools, "tool")
	if failed > 0 {
		s += " · " + strconv.Itoa(failed) + " failed"
	}
	return s
}

// renderBelaiSystemLines adds a system notice's wrapped, indented lines to
// the panel body and returns updated owner, type and line slices.
func renderBelaiSystemLines(msg Message, owner, inner int, bar string, barCol, icol int, groupCopyable bool, owners []int, isSystem []bool, bodyLines []string, lm LineMap) ([]int, []bool, []string, LineMap) {
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

// renderBelaiActivityLines adds a role-manager activity row to the panel
// body. Unlike a system notice, the activity line is built from plain Segs and
// wrapped with wrapSegs, then rendered through Row.Render so the outcome word
// carries its tone colour while the LineMap is measured while the text is
// still plain.
func renderBelaiActivityLines(msg Message, owner, inner int, bar string, barCol, icol int, expandAll bool, groupCopyable bool, owners []int, isSystem []bool, bodyLines []string, lm LineMap) ([]int, []bool, []string, LineMap) {
	// The row is one line whatever the width: the summary gives way first.
	// Expanded, a second dim line names the activity key and the full
	// provider/model behind the decision.
	lines := [][]Seg{rmRowSegs(msg, max(inner-icol, 1))}
	if expandAll {
		var parts []string
		if msg.Activity != "" {
			parts = append(parts, "["+msg.Activity+"]")
		}
		if msg.Provider != "" && msg.Model != "" {
			parts = append(parts, "["+msg.Provider+"/"+msg.Model+"]")
		}
		if c := msg.RMMeta.Category; c != "" {
			parts = append(parts, c)
		}
		if msg.RMMeta.HasScore {
			parts = append(parts, fmt.Sprintf("score %d%%", msg.RMMeta.Score))
		}
		if cause := msg.RMMeta.Cause; cause != "" && cause != string(rolemanager.CauseNone) {
			parts = append(parts, cause)
		}
		if len(parts) > 0 {
			clipped, _ := clipSegs([]Seg{NewSeg(strings.Join(parts, " "), ColorLow)}, max(inner-icol-2, 1))
			lines = append(lines, append([]Seg{NewSeg("  ", nil)}, clipped...))
		}
	}
	first := true
	for _, lineSegs := range lines {
		if len(lineSegs) == 0 {
			lineSegs = []Seg{}
		}
		prefix := ""
		if !first {
			prefix = spaces(icol)
		}
		segs := append([]Seg{NewSeg(prefix, ColorMuted)}, lineSegs...)
		row := Row{Segs: segs, Gutter: visibleLen(prefix)}
		styled, sl := row.Render(inner)
		pad := inner - visibleLen(segsPlain(segs))
		if pad < 0 {
			pad = 0
		}
		rendered := bar + " " + styled + spaces(pad) + " " + bar
		bodyLines = append(bodyLines, rendered)

		sl.Col += barCol
		sl.Owner = owner
		sl.Copyable = groupCopyable
		owners = append(owners, owner)
		isSystem = append(isSystem, true)
		lm = append(lm, sl)
		first = false
	}
	return owners, isSystem, bodyLines, lm
}

// toneColor maps an activity tone to its outcome colour.
func toneColor(t rolemanager.Tone) lipgloss.TerminalColor {
	switch t {
	case rolemanager.ToneClear:
		return ColorTeal
	case rolemanager.ToneCaution:
		return ColorAmber
	case rolemanager.ToneBlocked:
		return ColorDanger
	default:
		return ColorMuted
	}
}

// renderBelaiToolLines adds a tool result's existing row rendering to the
// panel body and returns updated owner, type and line slices. The tool row is
// rendered at the panel's inner width so that status alignment and content
// wrapping fit exactly between the borders.
func renderBelaiToolLines(msg Message, owner, inner int, bar string, barCol int, expandAll bool, groupCopyable bool, owners []int, isSystem []bool, bodyLines []string, lm LineMap) ([]int, []bool, []string, LineMap) {
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

// belaiHidden joins the raw text of the notices whose rows were hidden,
// deduplicating so a notice that still has visible rows is not duplicated in a
// selection over the hint.
func belaiHidden(msgs []Message, owners []int, from int) string {
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
