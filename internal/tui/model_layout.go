package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modeltest"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The /model roles screen is a master-detail layout that fits one terminal
// screen: a rail lists the roles, the detail pane edits the selected role, and
// a short status strip reports the last test. Nothing on it depends on
// scrolling the whole page, so every control stays reachable in a 24-row
// terminal. Long lists (the routing pool) scroll inside their own pane.
//
// Only the presentation and navigation live here. Every edit still goes
// through the same stage*/mutate* functions as before, keyed by the role's own
// save target, so what is written and where is unchanged.

const (
	// modelRailWide is the rail width beside the detail pane.
	modelRailWide = 32
	// modelSideBySide is the content width from which the rail sits beside the
	// detail pane; narrower terminals get a tab strip above it.
	modelSideBySide = 100
	// modelPageChrome is the rows the page spends outside the panes: the
	// view's own padding (2), the header line and the help line.
	modelPageChrome = 4
)

// modelRoleOrder lists the roles in the order rows() builds them.
func modelRoleOrder(rows []modelRow) []modelRole {
	var out []modelRole
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1] != r.role {
			out = append(out, r.role)
		}
	}
	return out
}

// modelRoleRange returns the first and last row index of a role, or -1, -1.
func modelRoleRange(rows []modelRow, role modelRole) (int, int) {
	lo, hi := -1, -1
	for i, r := range rows {
		if r.role != role {
			continue
		}
		if lo < 0 {
			lo = i
		}
		hi = i
	}
	return lo, hi
}

// modelRoleTitle is the role's name on the rail and the tab strip.
func modelRoleTitle(role modelRole) string {
	switch role {
	case roleAgent:
		return "Agent"
	case roleFast:
		return "Fast tier"
	case roleClassifier:
		return "Classifier"
	case roleRouting:
		return "Routing"
	case rolePosture:
		return "Posture"
	case roleMCP:
		return "Built-in MCP"
	}
	return string(role)
}

// modelRoleScopeLabel is the short save target shown beside a role.
func (a *App) modelRoleScopeLabel(role modelRole) string {
	if role == rolePosture {
		return "prefs"
	}
	return a.roleScope(role)
}

// modelRoleValue is the one-line summary of a role for the rail.
func (a *App) modelRoleValue(role modelRole) string {
	switch role {
	case roleAgent:
		if a.cfg.Model == "" {
			return "—"
		}
		return a.cfg.Model
	case roleFast:
		return a.resolvedFastLabel()
	case roleClassifier:
		return a.securityLabel()
	case roleRouting:
		kind := config.RoutingDefined
		if a.settings.Routing != nil && a.settings.Routing.Kind != "" {
			kind = a.settings.Routing.Kind
		}
		pool := 0
		for _, uc := range routingUseCaseKeys() {
			if p, m := a.routingUseCaseTarget(uc); p != "" || m != "" {
				pool++
			}
		}
		return fmt.Sprintf("%s · %d in pool", kind, pool)
	case rolePosture:
		return "guardrails " + boolLabel(a.guardrailsEnabled()) + " · ask " + boolLabel(a.askEnabled())
	case roleMCP:
		return "clef " + boolLabel(a.settings.ClefMCPEnabled()) + " · skip " + mcpSkipAskValue(a.settings)
	}
	return ""
}

// box draws lines inside a hairline border w cells wide. With h > 0 it is
// exactly h rows tall: short content is padded and long content is clipped, so
// two boxes side by side always line up and never push the page taller.
func box(lines []string, w, h int) string {
	iw := max(w-2, 1)
	if h > 0 {
		body := max(h-2, 0)
		for len(lines) < body {
			lines = append(lines, "")
		}
		lines = lines[:body]
	}
	edge := components.LineStyle
	var b strings.Builder
	b.WriteString(edge.Render("┌"+strings.Repeat("─", iw)+"┐") + "\n")
	for _, l := range lines {
		l = ansi.Truncate(l, iw, "…")
		b.WriteString(edge.Render("│") + padRight(l, iw) + edge.Render("│") + "\n")
	}
	b.WriteString(edge.Render("└" + strings.Repeat("─", iw) + "┘"))
	return b.String()
}

// modelInEffect is what the running session resolves, per use.
func (a *App) modelInEffect() [][2]string {
	verdicts := a.resolvedFastLabel()
	drafting := "work model"
	if a.cfg.Routing.Kind == config.RoutingRouted && len(a.cfg.Routing.Candidates) > 0 {
		// A fast use case skips Jev whenever a fast tier exists
		// (run.NewRoleClassifier); only without one does Jev pick for it.
		if a.cfg.Routing.Fast == nil {
			verdicts = "Jev picks from pool, else work model"
		}
		drafting = "Jev picks from pool, else work model"
	}
	return [][2]string{
		{"work", a.providerDisplayLabel(a.cfg.Provider) + " · " + a.cfg.Model},
		{"verdicts", verdicts},
		{"drafting", drafting},
		{"security", a.securityLabel()},
	}
}

// modelRailLines renders the roles list. Two lines per role (name and scope,
// then its value) when the pane has room, one line otherwise.
func (a *App) modelRailLines(rows []modelRow, cur modelRole, iw int, compact bool) []string {
	lines := []string{components.MutedStyle.Render("ROLES")}
	for _, role := range modelRoleOrder(rows) {
		selected := role == cur
		scope := a.modelRoleScopeLabel(role)
		name := modelRoleTitle(role)
		val := a.modelRoleValue(role)
		if compact {
			w := iw - 2 - ansi.StringWidth(name) - 1
			line := components.Cursor(selected)
			if selected {
				line += components.AccentStyle.Bold(true).Render(name)
			} else {
				line += name
			}
			lines = append(lines, line+" "+components.MutedStyle.Render(truncTail(val, max(w, 4))))
			continue
		}
		gap := max(iw-2-ansi.StringWidth(name)-ansi.StringWidth(scope), 1)
		nameStyled := name
		if selected {
			nameStyled = components.AccentStyle.Bold(true).Render(name)
		}
		lines = append(lines, components.Cursor(selected)+nameStyled+strings.Repeat(" ", gap)+components.MutedStyle.Render(scope))
		lines = append(lines, "  "+components.MutedStyle.Render(truncTail(val, iw-2)))
	}
	return lines
}

// modelTabStrip renders the roles as one line for narrow terminals.
func (a *App) modelTabStrip(rows []modelRow, cur modelRole, w int) string {
	order := modelRoleOrder(rows)
	var parts []string
	pos := 0
	for i, role := range order {
		name := modelRoleTitle(role)
		if role == cur {
			pos = i + 1
			parts = append(parts, components.Chip(name, components.ColorTeal))
			continue
		}
		parts = append(parts, components.MutedStyle.Render(" "+name+" "))
	}
	line := strings.Join(parts, " ")
	count := components.MutedStyle.Render(fmt.Sprintf("%d/%d", pos, len(order)))
	if gap := w - ansi.StringWidth(line) - ansi.StringWidth(count); gap > 1 {
		return line + strings.Repeat(" ", gap) + count
	}
	return ansi.Truncate(line, w, "…")
}

// modelDetailLines renders the selected role's pane: the group header, then
// its fields windowed to the rows that are left. ih <= 0 means unbounded.
func (a *App) modelDetailLines(rows []modelRow, role modelRole, iw, ih int) []string {
	g := a.modelGroupFor(role, rows)
	header := strings.Split(a.modelGroupHeader(g, iw), "\n")

	labelW := len("provider")
	srcW := 0
	for _, r := range rows {
		if r.role != role {
			continue
		}
		labelW = max(labelW, ansi.StringWidth(r.label))
		if g.perRow {
			srcW = max(srcW, ansi.StringWidth("set in "+setIn(r.src, g.scope)))
		}
	}
	labelW += 2
	valW := iw - 2 - len(modelIndent) - labelW
	if srcW > 0 {
		valW -= srcW + 2
	}
	valW = max(valW, 12)

	type fieldLine struct {
		text string
		idx  int // row index, or -1 for a label line
	}
	var fields []fieldLine
	sel := -1
	routed := a.settings.Routing != nil && a.settings.Routing.Kind == config.RoutingRouted
	for i, r := range rows {
		if r.role != role {
			continue
		}
		g := g
		isRoute := strings.HasPrefix(r.key, "route:")
		if isRoute && !strings.HasPrefix(safeRow(rows, i-1).key, "route:") {
			fields = append(fields, fieldLine{components.MutedStyle.Render("  candidate pool"), -1})
		}
		selected := i == a.modelState.selected
		label := modelIndent + padRight(r.label, labelW)
		rawValue := r.value
		if p, ok := a.modelState.pendingProvider[r.role]; ok && r.key == "provider" {
			rawValue = p + "  · pick a model to test and save"
		}
		rawValue += a.testingSuffix(r.role, r.key)
		value := truncTail(rawValue, valW)
		// Pool rows stay editable under kind defined, but they are not in use,
		// so they read like disabled ones.
		dim := r.disabled || (isRoute && !routed)
		switch {
		case selected:
			label = components.AccentStyle.Bold(true).Render(label)
			value = components.EmphStyle.Render(value)
		case dim:
			label = components.MutedStyle.Render(label)
			value = components.MutedStyle.Render(value)
		default:
			label = components.MutedStyle.Render(label)
		}
		line := components.Cursor(selected) + label + value
		if src := setIn(r.src, g.scope); g.perRow && src != "" {
			style := components.MutedStyle
			if outranks(src, g.scope) {
				style = components.WarnStyle
			}
			gap := valW - ansi.StringWidth(truncTail(rawValue, valW)) + 2
			line += strings.Repeat(" ", gap) + style.Render("set in "+src)
		}
		if selected {
			sel = len(fields)
		}
		fields = append(fields, fieldLine{line, i})
	}

	avail := len(fields)
	if ih > 0 {
		avail = max(ih-len(header), 1)
	}
	out := append([]string{}, header...)
	if len(fields) <= avail {
		a.modelState.fieldScroll = 0
		for _, f := range fields {
			out = append(out, f.text)
		}
		return out
	}
	// The last row of the pane is the scroll marker, so the window is one
	// shorter than what is left.
	win := max(avail-1, 1)
	start := windowStart(a.modelState.fieldScroll, max(sel, 0), len(fields), win)
	a.modelState.fieldScroll = start
	for _, f := range fields[start : start+win] {
		out = append(out, f.text)
	}
	above, below := start, len(fields)-start-win
	var marks []string
	if above > 0 {
		marks = append(marks, fmt.Sprintf("↑ %d above", above))
	}
	if below > 0 {
		marks = append(marks, fmt.Sprintf("↓ %d below", below))
	}
	return append(out, components.MutedStyle.Render("  "+strings.Join(marks, " · ")))
}

// modelStatusLines is the strip under the panes: a compact account of the last
// test (the full step list is on `d`), the availability note and any error.
func (a *App) modelStatusLines(w int) []string {
	var out []string
	if run := a.modelTest; run != nil {
		out = append(out, a.modelTestCompact(run, w)...)
	}
	if a.avail.note != "" {
		out = append(out, components.MutedStyle.Render(ansi.Truncate(a.avail.note, w, "…")))
	}
	if a.modelState.errorMsg != "" {
		out = append(out, components.DangerStyle.Render(ansi.Truncate("✗ "+a.modelState.errorMsg, w, "…")))
	}
	return out
}

// modelTestCompact reduces the test panel to what needs attention: the title,
// the steps that are running, warned or failed, and the outcome with its keys.
func (a *App) modelTestCompact(run *modelTestRun, w int) []string {
	var out []string
	title := "testing " + run.change.label
	switch run.outcome {
	case "saved":
		title = ""
	case "not-saved", "write-failed":
		title = "tested " + run.change.label
	case "cancelled":
		title = "test cancelled · " + run.change.label
	}
	if title != "" {
		out = append(out, components.EmphStyle.Render(ansi.Truncate(title, w, "…")))
	}
	frame := a.modelTestFrame(run)
	for _, s := range run.steps {
		switch s.status {
		case modeltest.StatusRunning, modeltest.StatusWarn, modeltest.StatusFail:
			out = append(out, a.modelTestStepLine(run, s, w, frame))
			if s.status == modeltest.StatusRunning && run.dlTotal > 0 {
				out = append(out, "    "+a.downloadBar(run, w-4))
			}
		}
	}
	if o := run.confirm; o != nil {
		out = append(out, components.WarnStyle.Render(ansi.Truncate(
			fmt.Sprintf("Download %s (%s) for %s?", o.What, modeltest.Sizes(o.Size), o.Label), w, "…")))
		if o.Dest != "" {
			out = append(out, components.MutedStyle.Render(ansi.Truncate("  into "+o.Dest, w, "…")))
		}
		return append(out, components.HelpBar("y", "download", "n", "cancel"))
	}
	switch run.outcome {
	case "running":
		out = append(out, components.MutedStyle.Render("  nothing is saved until every check passes · esc cancels"))
	case "saved":
		out = append(out, components.AccentStyle.Render(ansi.Truncate("✓ saved "+run.change.label+" → "+a.savedWhere(run.change), w, "…")))
		if warns := countStatus(run.steps, modeltest.StatusWarn); warns > 0 {
			out = append(out, components.WarnStyle.Render(fmt.Sprintf("  saved with %d warning%s above", warns, pluralS(warns))))
		}
	case "not-saved":
		out = append(out, components.DangerStyle.Render(ansi.Truncate("✗ not saved — "+run.change.was+" stays in effect", w, "…")))
	case "write-failed":
		out = append(out, components.DangerStyle.Render(ansi.Truncate("✗ the test passed but the settings file could not be written: "+run.note, w, "…")))
	case "cancelled":
		out = append(out, components.MutedStyle.Render("  cancelled; nothing was saved"))
	}
	if run.note != "" && run.outcome != "write-failed" {
		out = append(out, components.MutedStyle.Render(ansi.Truncate("  "+run.note, w, "…")))
	}
	if run.outcome != "running" {
		if h := strings.TrimRight(a.hintLines(run.hints, w), "\n"); h != "" {
			out = append(out, strings.Split(h, "\n")...)
		}
	}
	return out
}

// modelTestFrame is the spinner glyph for the current instant.
func (a *App) modelTestFrame(run *modelTestRun) string {
	if !a.settings.SpinnerEnabled() {
		return "○"
	}
	return modelTestSpinner[int(time.Since(run.started)/(100*time.Millisecond))%len(modelTestSpinner)]
}

// modelScreen is the roles screen. h is the terminal height, 0 when unknown
// (nothing is clipped then).
func (a *App) modelScreen(rows []modelRow, w, h int) string {
	inner := w - 2 // the view's own Padding(1)
	if a.modelState.selected >= len(rows) {
		a.modelState.selected = max(len(rows)-1, 0)
	}
	cur := safeRow(rows, a.modelState.selected).role

	var b strings.Builder
	title := components.AccentStyle.Render("◈") + " " + components.EmphStyle.Render("Model Roles")
	b.WriteString(title + "\n")

	status := a.modelStatusLines(inner)
	if h > 0 {
		// A status strip never crowds the panes out: keep its tail, which
		// carries the outcome and its keys.
		if limit := max((h-modelPageChrome)*2/5, 3); len(status) > limit {
			status = status[len(status)-limit:]
		}
	}
	bodyH := 0
	if h > 0 {
		bodyH = max(h-modelPageChrome-len(status), 7)
	}

	sideBySide := inner >= modelSideBySide
	if sideBySide {
		railW := modelRailWide
		detailW := inner - railW - 1
		detail := box(a.modelDetailLines(rows, cur, detailW-2, bodyH-2), detailW, bodyH)

		compact := bodyH > 0 && bodyH < 13
		railLines := a.modelRailLines(rows, cur, railW-2, compact)
		railH := len(railLines) + 2
		effect := a.modelInEffect()
		effectH := len(effect) + 3
		var rail string
		switch {
		case bodyH == 0:
			rail = box(railLines, railW, 0) + "\n" + box(a.effectLines(effect, railW-2), railW, 0)
		case bodyH-railH >= effectH:
			rail = box(railLines, railW, railH) + "\n" + box(a.effectLines(effect, railW-2), railW, bodyH-railH)
		default:
			rail = box(railLines, railW, bodyH)
		}
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, rail, " ", detail))
	} else {
		b.WriteString(a.modelTabStrip(rows, cur, inner) + "\n")
		paneH := 0
		if bodyH > 0 {
			paneH = bodyH - 1
		}
		b.WriteString(box(a.modelDetailLines(rows, cur, inner-2, paneH-2), inner, paneH))
	}
	for _, l := range status {
		b.WriteString("\n" + l)
	}
	b.WriteString("\n" + a.modelHelpLine(inner))
	return b.String()
}

// effectLines renders the IN EFFECT box body.
func (a *App) effectLines(effect [][2]string, iw int) []string {
	lines := []string{components.MutedStyle.Render("IN EFFECT")}
	for _, l := range effect {
		lines = append(lines, components.MutedStyle.Render(padRight(l[0], 9))+truncTail(l[1], max(iw-9, 4)))
	}
	return lines
}

// modelLogScreen shows the whole test, step by step, in place of the panes.
func (a *App) modelLogScreen(w, h int) string {
	inner := w - 2
	var b strings.Builder
	b.WriteString(components.AccentStyle.Render("◈") + " " + components.EmphStyle.Render("Model test log") + "\n")
	lines := strings.Split(strings.TrimRight(a.modelTestPanel(inner), "\n"), "\n")
	if h > 0 {
		if keep := max(h-modelPageChrome, 3); len(lines) > keep {
			lines = lines[len(lines)-keep:]
		}
	}
	b.WriteString(box(lines, inner, 0))
	b.WriteString("\n" + ansi.Truncate(components.HelpBar("d", "back", "esc", "back"), inner, "…"))
	return b.String()
}

// securityLabel names the security guard for the rail and IN EFFECT: the local
// gates when the models stack is on, then the phase-3 model.
func (a *App) securityLabel() string {
	guard := run.GuardConfig(a.cfg)
	label := a.providerDisplayLabel(guard.Provider) + " · " + guard.Model
	if sc := a.resolvedSecurityClassifier(); sc.Kind == "models" {
		if sc.Phase3On {
			return "local gates + " + label
		}
		return "local gates"
	}
	return label
}

// modelHelpLine is the key bar. When the width cannot hold every key it drops
// the least important first, so the ones that matter most (movement, edit,
// back, and the keys a test result offers) always stay on screen.
func (a *App) modelHelpLine(w int) string {
	keys := []glKey{
		{"↑↓", "field", 1}, {"←→", "role", 1}, {"⏎", "edit", 1},
		{"tab", "mode", 6}, {"s", "save to", 3}, {"c", "clear", 5}, {"p", "providers", 4},
	}
	if a.modelTest != nil {
		keys = append(keys, glKey{"d", "test log", 2})
	}
	if extra := a.modelTestHelp(); len(extra) >= 2 {
		keys = append(keys, glKey{extra[0], extra[1], 2})
	}
	keys = append(keys, glKey{"esc", "back", 1})
	return glHelpLine(keys, w)
}
