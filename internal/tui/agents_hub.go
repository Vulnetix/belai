package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/tui/components"
)

// The /agents hub is one screen with three tabs: what is running now, the
// saved profiles, and the audit trail of every step an agent took. 1, 2 and 3
// or tab move between them; each tab keeps its own selection.

// agentTabStrip renders the tab row with a count on each tab.
func (a *App) agentTabStrip(w int) string {
	live, parked := a.agentCounts()
	counts := [agentTabCount]int{live + parked, len(a.agentState.profiles), len(a.ledger.audit)}
	var parts, plain []string
	for i, name := range agentTabNames {
		label := fmt.Sprintf("%s %d", name, counts[i])
		if i == a.agentState.tab {
			label = "[ " + label + " ]"
			parts = append(parts, components.EmphStyle.Render(label))
		} else {
			parts = append(parts, components.MutedStyle.Render(label))
		}
		plain = append(plain, label)
	}
	left := strings.Join(parts, "   ")
	meta := "1 2 3 · tab"
	pad := w - lipgloss.Width(strings.Join(plain, "   ")) - lipgloss.Width(meta)
	if pad < 2 {
		return ansi.Truncate(left, w, "")
	}
	return left + strings.Repeat(" ", pad) + components.MutedStyle.Render(meta)
}

// handleAgentTabKey switches tabs from any list. It reports whether it took
// the key.
func (a *App) handleAgentTabKey(m tea.KeyMsg) (tea.Cmd, bool) {
	switch m.String() {
	case "1", "2", "3":
		a.agentState.tab = int(m.String()[0] - '1')
	case "tab":
		a.agentState.tab = (a.agentState.tab + 1) % agentTabCount
	case "shift+tab":
		a.agentState.tab = (a.agentState.tab + agentTabCount - 1) % agentTabCount
	default:
		return nil, false
	}
	a.agentState.errorMsg = ""
	return nil, true
}

// hubRows is the number of list rows a hub tab can show.
func (a *App) hubRows() int {
	if n := a.height - 12; n > 3 {
		return n
	}
	return 3
}

// agentLiveView lists every agent seen this session, working ones first.
func (a *App) agentLiveView(w int) string {
	var b strings.Builder
	list := a.liveAgents()
	if len(list) == 0 {
		b.WriteString(components.MutedStyle.Render("  no agents this session · s on the profiles tab starts one") + "\n")
		b.WriteString("\n" + components.HelpBar("2", "profiles", "esc", "back") + "\n")
		return b.String()
	}
	a.agentState.liveSel = clamp(a.agentState.liveSel, 0, len(list)-1)
	rows := a.hubRows() / 2
	start := windowStart(0, a.agentState.liveSel, len(list), rows)
	end := min(start+rows, len(list))
	for i := start; i < end; i++ {
		b.WriteString(a.renderLiveRow(list[i], i == a.agentState.liveSel, w))
	}
	if end < len(list) {
		b.WriteString(components.MutedStyle.Render(fmt.Sprintf("  → %d more", len(list)-end)) + "\n")
	}
	b.WriteString("\n" + components.HelpBar("↑↓", "move", "⏎", "follow", "p", "pause/resume", "x", "stop", "a", "audit", "esc", "back") + "\n")
	return b.String()
}

// renderLiveRow is two lines: the loop state, then the latest output.
func (a *App) renderLiveRow(l *agentLive, selected bool, w int) string {
	glyph := a.pulseGlyph(l.State)
	label := fmt.Sprintf("%-24s", truncateValue(l.Label, 24))
	if selected {
		label = components.EmphStyle.Render(label)
	}
	head := components.Cursor(selected) + agentStateStyle(l.State).Render(glyph) + " " + label +
		components.MutedStyle.Render(fmt.Sprintf("%-12s", l.Kind)) +
		agentStateStyle(l.State).Render(fmt.Sprintf("%-10s", l.State))
	var parts []string
	if step := agentStep(l); step != "" {
		if l.Tool != "" {
			parts = append(parts, components.WarnStyle.Render(step))
		} else {
			parts = append(parts, components.MutedStyle.Render(step))
		}
	}
	for _, p := range []string{agentIterLabel(l), toolCount(l), compactDuration(agentElapsed(l))} {
		if p != "" {
			parts = append(parts, components.MutedStyle.Render(p))
		}
	}
	if l.Errors > 0 {
		parts = append(parts, components.DangerStyle.Render(countOf(l.Errors, "error")))
	}
	line := head + strings.Join(parts, components.MutedStyle.Render("  "))
	out := ansi.Truncate(line, w, "…") + "\n"
	if l.Last != "" {
		out += ansi.Truncate(components.MutedStyle.Render("    "+l.Last), w, "…") + "\n"
	}
	return out
}

func toolCount(l *agentLive) string {
	if l.Tools == 0 {
		return ""
	}
	return countOf(l.Tools, "tool")
}

// agentStateStyle colours a state the same way everywhere.
func agentStateStyle(state string) lipgloss.Style {
	switch state {
	case "running":
		return components.AccentStyle
	case "queued", "idle":
		return components.MutedStyle
	case "paused":
		return components.WarnStyle
	case "failed", "cancelled", "stopped":
		return components.DangerStyle
	default:
		return lipgloss.NewStyle().Foreground(components.ColorTealSoft)
	}
}

func (a *App) selectedLiveAgent() (*agentLive, bool) {
	list := a.liveAgents()
	if a.agentState.liveSel < 0 || a.agentState.liveSel >= len(list) {
		return nil, false
	}
	return list[a.agentState.liveSel], true
}

func (a *App) handleAgentLiveKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "esc":
		a.pop()
		return nil
	case "up", "k":
		if a.agentState.liveSel > 0 {
			a.agentState.liveSel--
		}
		return nil
	case "down", "j":
		if a.agentState.liveSel < len(a.ledger.order)-1 {
			a.agentState.liveSel++
		}
		return nil
	}
	l, ok := a.selectedLiveAgent()
	if !ok {
		return nil
	}
	switch m.String() {
	case "enter":
		a.followAgent(l.ID)
	case "a":
		a.agentState.auditFilter = l.ID
		a.agentState.auditSel = 0
		a.agentState.tab = agentTabAudit
	case "p":
		name, bg := strings.CutPrefix(l.ID, "bg:")
		if !bg {
			a.agentState.errorMsg = "only background agents pause"
			return nil
		}
		if l.State == "paused" {
			return a.resumeAgent(name)
		}
		a.pauseAgent(name)
	case "x":
		if name, bg := strings.CutPrefix(l.ID, "bg:"); bg {
			if agentStateRank(l.State) < 3 {
				a.stopAgent(name)
			}
			return nil
		}
		if l.State == "queued" || l.State == "running" {
			a.cancelSubagent(l.ID)
		} else {
			a.dismissSubagent(l.ID)
		}
	}
	return nil
}

// auditRows returns the trail newest first, narrowed to the filter.
func (a *App) auditRows() []auditEntry {
	out := make([]auditEntry, 0, len(a.ledger.audit))
	for i := len(a.ledger.audit) - 1; i >= 0; i-- {
		e := a.ledger.audit[i]
		if a.agentState.auditFilter != "" && e.ID != a.agentState.auditFilter {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (a *App) agentAuditView(w int) string {
	var b strings.Builder
	if f := a.agentState.auditFilter; f != "" {
		b.WriteString(components.MutedStyle.Render("  only ") + components.AccentStyle.Render(subagentGutterLabel(f)) + components.MutedStyle.Render(" · c shows every agent") + "\n")
	}
	rows := a.auditRows()
	if len(rows) == 0 {
		b.WriteString(components.MutedStyle.Render("  nothing recorded yet") + "\n")
		b.WriteString("\n" + components.HelpBar("1", "running", "esc", "back") + "\n")
		return b.String()
	}
	a.agentState.auditSel = clamp(a.agentState.auditSel, 0, len(rows)-1)
	n := a.hubRows()
	start := windowStart(0, a.agentState.auditSel, len(rows), n)
	end := min(start+n, len(rows))
	for i := start; i < end; i++ {
		e := rows[i]
		selected := i == a.agentState.auditSel
		who := fmt.Sprintf("%-16s", truncateValue(subagentGutterLabel(e.ID), 16))
		if selected {
			who = components.EmphStyle.Render(who)
		}
		line := components.Cursor(selected) + components.MutedStyle.Render(e.At.Format("15:04:05")+"  ") + who +
			auditStyle(e.What).Render(fmt.Sprintf("%-7s", e.What)) + e.Text
		b.WriteString(ansi.Truncate(line, w, "…") + "\n")
	}
	if end < len(rows) {
		b.WriteString(components.MutedStyle.Render(fmt.Sprintf("  → %d older", len(rows)-end)) + "\n")
	}
	b.WriteString("\n" + components.HelpBar("↑↓", "move", "⏎", "follow", "c", "all agents", "esc", "back") + "\n")
	return b.String()
}

func auditStyle(what string) lipgloss.Style {
	switch what {
	case "tool":
		return components.WarnStyle
	case "error":
		return components.DangerStyle
	case "state":
		return components.AccentStyle
	default:
		return components.MutedStyle
	}
}

func (a *App) handleAgentAuditKey(m tea.KeyMsg) tea.Cmd {
	rows := a.auditRows()
	switch m.String() {
	case "esc":
		a.pop()
	case "up", "k":
		if a.agentState.auditSel > 0 {
			a.agentState.auditSel--
		}
	case "down", "j":
		if a.agentState.auditSel < len(rows)-1 {
			a.agentState.auditSel++
		}
	case "c":
		a.agentState.auditFilter = ""
		a.agentState.auditSel = 0
	case "enter":
		if a.agentState.auditSel >= 0 && a.agentState.auditSel < len(rows) {
			a.followAgent(rows[a.agentState.auditSel].ID)
		}
	}
	return nil
}

// agentNotice reports an outcome where the user is looking: on the hub it is
// the error line, anywhere else a system line.
func (a *App) agentNotice(msg string) {
	if a.view == viewAgent {
		a.agentState.errorMsg = msg
		return
	}
	a.addSystem(msg)
}

// startAgentProfile starts a saved profile as a background agent.
func (a *App) startAgentProfile(name string) tea.Cmd {
	if a.bgManager == nil {
		a.agentNotice("agent start: no background manager configured; background agents need provider credentials")
		return nil
	}
	p, err := agentprofile.Load(name)
	if err != nil {
		a.agentNotice("agent start: " + err.Error())
		return nil
	}
	if err := a.bgManager.Start(name, p); err != nil {
		a.agentNotice("agent start: " + err.Error())
		return nil
	}
	a.registerAgentActivity(name, name, a.workdir)
	return a.noteAgentStarted(name)
}

// stopAgent stops a background agent.
func (a *App) stopAgent(name string) {
	if a.bgManager == nil {
		a.agentNotice("agent: no background manager configured")
		return
	}
	if err := a.bgManager.Stop(name); err != nil {
		a.agentNotice("agent stop: " + err.Error())
		return
	}
	a.noteAgentStopped(name)
}

// pauseAgent parks a looping background agent at its next boundary.
func (a *App) pauseAgent(name string) {
	if a.bgManager == nil {
		a.agentNotice("agent: no background manager configured")
		return
	}
	if err := a.bgManager.Pause(name); err != nil {
		a.agentNotice("agent pause: " + err.Error())
		return
	}
	a.syncBackgroundLedger()
	a.addSystem("‖ " + name + " paused")
}

// resumeAgent wakes a paused background agent.
func (a *App) resumeAgent(name string) tea.Cmd {
	if a.bgManager == nil {
		a.agentNotice("agent: no background manager configured")
		return nil
	}
	if err := a.bgManager.Resume(name); err != nil {
		a.agentNotice("agent resume: " + err.Error())
		return nil
	}
	a.syncBackgroundLedger()
	a.addSystem("▸ " + name + " resumed")
	// The event watcher stays armed through a pause, so only the pulse needs
	// waking.
	return a.armAgentPulse()
}
