package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/signet/internal/activity"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/bgproc"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/processlib"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/scanartifacts"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/tui/components"
)

// activitySend is one finished activity's output, queued to round-trip to the
// model once the in-flight turn goes idle.
type activitySend struct {
	label string
	atts  []run.Attachment
	// directive is the harness-recorded facts and ask for this output.
	directive string
}

// activityEventMsg carries one registry delta into the TUI loop.
type activityEventMsg activity.Activity

const (
	minRunsPanelOpenHeight = 6
)

const (
	tabActivity  = 0
	tabSubagents = 1
	tabProcesses = 2
	tabGit       = 3
	tabCI        = 4 // offered only while the branch has a PR/MR (ciTabVisible)
	tabCount     = 5
)

// runsItem is a row in the runs panel. It unifies activities and subagents so
// the same selection and windowing logic applies to both tabs.
type runsItem struct {
	ID          string
	Label       string
	Detail      string
	State       string
	ProjectRoot string
}

// runsPanelHeight returns the rows the runs panel contributes below the
// viewport. Zero when the panel is closed. When open it is bounded by
// a.height/3 and clamped so it never exceeds the available frame.
func (a *App) runsPanelHeight() int {
	if !a.runsOpen {
		return 0
	}
	maxRows := a.height/3 - 3
	if maxRows < 0 {
		maxRows = 0
	}
	items := a.runsItems()
	visible := min(len(items), maxRows)
	if visible < 1 {
		visible = 1 // header/help/rule still render one placeholder row
	}
	return min(a.height, 3+len(a.runsSummary())+visible)
}

// runsItems returns the items for the current runs tab, with a nil guard on the
// activity registry.
func (a *App) runsItems() []runsItem {
	switch a.runsTab {
	case tabSubagents:
		return a.subagentItems()
	case tabProcesses:
		return a.processItems()
	case tabGit:
		return a.gitItems()
	case tabCI:
		return a.ciItems()
	default:
		return a.activityItems()
	}
}

// processItems lists the running supervised processes that have a process
// library entry. It mirrors the /processes screen but shows only live runs.
func (a *App) processItems() []runsItem {
	if a.procManager == nil {
		return nil
	}
	running := make(map[string]bgproc.Process)
	for _, p := range a.procManager.List() {
		if p.State == bgproc.StateRunning {
			running[p.Name] = p
		}
	}
	if len(running) == 0 {
		return nil
	}
	global, _ := processlib.Load(config.ScopeGlobal, a.workdir)
	project, _ := processlib.Load(config.ScopeProject, a.workdir)
	merged := processlib.Merge(global.Entries, project.Entries)
	items := make([]runsItem, 0, len(running))
	for _, e := range merged {
		if p, ok := running[e.Name]; ok {
			detail := p.State.Label()
			if p.PID != 0 {
				detail += fmt.Sprintf(" · pid %d", p.PID)
			}
			items = append(items, runsItem{
				ID:          "proc-" + p.ID,
				Label:       "!!" + e.Command,
				Detail:      detail,
				State:       string(p.State),
				ProjectRoot: p.Dir,
			})
		}
	}
	return items
}

func (a *App) activityItems() []runsItem {
	if a.activity == nil {
		return nil
	}
	acts := a.activity.List()
	items := make([]runsItem, 0, len(acts))
	for _, act := range acts {
		extra := ""
		if act.State == activity.StateDone || act.State == activity.StateFailed || act.State == activity.StateKilled {
			if act.State == activity.StateDone && len(act.Targets) > 0 {
				extra = fmt.Sprintf(" · %d artifacts", len(act.Targets))
			} else if act.ExitCode != 0 {
				extra = fmt.Sprintf(" · exit %d", act.ExitCode)
			}
		}
		items = append(items, runsItem{
			ID:          act.ID,
			Label:       act.Label,
			Detail:      string(act.State) + extra,
			State:       string(act.State),
			ProjectRoot: act.ProjectRoot,
		})
	}
	return items
}

func (a *App) subagentItems() []runsItem {
	items := []runsItem{{ID: "", Label: "main", Detail: "unfiltered", State: "main"}}
	for _, c := range a.subagents {
		items = append(items, runsItem{ID: c.ID, Label: c.Label, Detail: a.agentSummary(c.ID, c.State), State: c.State})
	}
	return items
}

// renderRunsPanel renders the bounded runs panel at full chat width. Every
// line is ansi-truncated before it is written, so long labels cannot wrap and
// push the frame beyond the terminal size.
func (a *App) renderRunsPanel() string {
	if !a.runsOpen {
		return ""
	}
	w := a.contentWidth()
	if w < 1 {
		w = 1
	}
	maxRows := a.height/3 - 3
	if maxRows < 1 {
		maxRows = 1
	}

	itemsWindow := a.windowedItems(maxRows)
	moreCount := 0
	if itemsWindow.all > maxRows && len(itemsWindow.items) > 0 {
		moreCount = itemsWindow.all - len(itemsWindow.items)
	}

	var b strings.Builder
	header := a.renderRunsTabHeader(a.runsTabNames(), w)
	b.WriteString(header)
	b.WriteString("\n")
	for _, line := range a.runsSummary() {
		b.WriteString(ansi.Truncate(renderSummaryLine(line), w, "") + "\n")
	}

	if len(itemsWindow.items) == 0 {
		placeholder := components.MutedStyle.Render("  no activities this turn")
		switch a.runsTab {
		case tabSubagents:
			placeholder = components.MutedStyle.Render("  no subagents this turn")
		case tabProcesses:
			placeholder = components.MutedStyle.Render("  no running processes")
		case tabGit, tabCI:
			placeholder = components.MutedStyle.Render("  " + a.forgePlaceholder())
		}
		b.WriteString(ansi.Truncate(placeholder, w, "") + "\n")
	} else {
		for i, it := range itemsWindow.items {
			if moreCount > 0 && i == len(itemsWindow.items)-1 {
				line := components.MutedStyle.Render(fmt.Sprintf("  → %d more", moreCount))
				b.WriteString(ansi.Truncate(line, w, "") + "\n")
				break
			}
			b.WriteString(a.renderRunsRow(it, i+itemsWindow.start, w) + "\n")
		}
	}

	help := a.runsPanelHelp()
	b.WriteString(ansi.Truncate(help, w, "") + "\n")
	b.WriteString(ansi.Truncate(components.Rule(w), w, "") + "\n")
	return b.String()
}

// windowedItems returns the visible slice of runs items and the logical start
// index, applying the same windowing rule as the /model picker.
func (a *App) windowedItems(maxRows int) itemsWindow {
	all := a.runsItems()
	n := len(all)
	if n == 0 {
		return itemsWindow{}
	}
	a.runsSel = clamp(a.runsSel, 0, n-1)
	if maxRows >= n {
		return itemsWindow{items: all, start: 0, all: n}
	}
	// Reserve the last visible row for "→ N more" when overflowing.
	rows := maxRows - 1
	start := windowStart(a.runsScroll, a.runsSel, n, rows)
	return itemsWindow{items: all[start : start+rows], start: start, all: n}
}

type itemsWindow struct {
	items []runsItem
	start int
	all   int
}

func (a *App) renderRunsTabHeader(names []string, w int) string {
	var parts []string
	var plainParts []string
	for i, n := range names {
		label := n
		if i == a.runsTab {
			label = "[ " + n + " ]"
		}
		plainParts = append(plainParts, label)
		if i == a.runsTab {
			parts = append(parts, components.EmphStyle.Render(label))
		} else {
			parts = append(parts, components.MutedStyle.Render(label))
		}
	}
	meta := "f9 close"
	if a.runsFocus {
		meta = "tab switch · f9 close"
	}
	plainLeft := strings.Join(plainParts, "  ")
	left := strings.Join(parts, components.MutedStyle.Render("  "))
	var b strings.Builder
	b.WriteString(left)
	if meta != "" {
		pad := w - visibleLen(plainLeft) - visibleLen(meta)
		if pad < 1 {
			pad = 1
		}
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(components.MutedStyle.Render(meta))
	}
	return ansi.Truncate(b.String(), w, "")
}

// visibleLen returns the displayed cell width of a string, ignoring ANSI
// escape sequences. It shadows the components helper for use inside panel tests.
func visibleLen(s string) int { return lipgloss.Width(s) }

func (a *App) renderRunsRow(it runsItem, logicalIdx, w int) string {
	selected := logicalIdx == a.runsSel
	marker := "  "
	if selected {
		marker = "▸ "
	}
	var stateColour lipgloss.TerminalColor
	switch it.State {
	case "running", "queued":
		stateColour = components.ColorMuted
	case "done", "main":
		stateColour = components.ColorTealSoft
	case "failed", "killed", "cancelled":
		stateColour = components.ColorAmber
	default:
		stateColour = components.ColorMuted
	}

	line := marker + it.Label
	if it.Detail != "" {
		line += "  " + components.MutedStyle.Render(it.Detail)
	}
	if selected {
		line = components.AccentStyle.Render(line)
	} else {
		line = lipgloss.NewStyle().Foreground(stateColour).Render(line)
	}
	return ansi.Truncate(line, w, "")
}

func (a *App) runsPanelHelp() string {
	if !a.runsFocus {
		return components.HelpBar("f9", "runs")
	}
	switch a.runsTab {
	case tabSubagents:
		return components.HelpBar("↑↓", "select", "⏎", "filter", "x", "cancel/dismiss", "esc", "unfocus", "tab", "switch")
	case tabProcesses:
		return components.HelpBar("↑↓", "select", "⏎", "view", "v", "view", "x", "stop", "r", "restart", "esc", "unfocus", "tab", "switch")
	case tabGit:
		return components.HelpBar("⏎", "switch", "a", "add", "x", "remove", "p", a.forgeNoun(), "c", "url", "r", "refresh")
	case tabCI:
		return components.HelpBar("↑↓", "select", "⏎/c", "copy link", "r", "refresh", "esc", "unfocus", "tab", "switch")
	default:
		return components.HelpBar("↑↓", "select", "⏎", "send output", "v", "view", "x", "kill", "t", "triage", "esc", "unfocus", "tab", "switch")
	}
}

// handleRunsPanelKey routes keys while the runs panel owns focus.
func (a *App) handleRunsPanelKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "esc":
		a.runsFocus = false
		return nil
	case "f9":
		a.runsOpen = false
		a.runsFocus = false
		return nil
	case "tab":
		a.runsTab = a.nextRunsTab()
		a.runsSel = 0
		a.runsScroll = 0
		if a.forgeTabActive() {
			return a.refreshForge(false)
		}
		return nil
	case "up", "k":
		if a.runsSel > 0 {
			a.runsSel--
		}
		return nil
	case "down", "j":
		if items := a.runsItems(); a.runsSel < len(items)-1 {
			a.runsSel++
		}
		return nil
	}

	switch a.runsTab {
	case tabActivity:
		return a.handleRunsActivityKey(m)
	case tabSubagents:
		return a.handleRunsSubagentKey(m)
	case tabProcesses:
		return a.handleRunsProcessKey(m)
	case tabGit:
		return a.handleRunsGitKey(m)
	case tabCI:
		return a.handleRunsCIKey(m)
	}
	return nil
}

func (a *App) handleRunsProcessKey(m tea.KeyMsg) tea.Cmd {
	items := a.processItems()
	if a.runsSel < 0 || a.runsSel >= len(items) {
		return nil
	}
	it := items[a.runsSel]
	pid := strings.TrimPrefix(it.ID, "proc-")
	switch m.String() {
	case "enter", "v":
		a.runsOutput.id = it.ID
		return a.push(viewRunsOutput)
	case "x":
		if a.procManager == nil {
			return nil
		}
		_ = a.procManager.Stop(pid)
	case "r":
		if a.procManager == nil {
			return nil
		}
		var p bgproc.Process
		for _, cp := range a.procManager.List() {
			if cp.ID == pid {
				p = cp
				break
			}
		}
		if p.ID == "" {
			return nil
		}
		_ = a.procManager.Stop(pid)
		_, _ = a.procManager.Start(p.Name, p.Command)
	}
	return nil
}

func (a *App) handleRunsActivityKey(m tea.KeyMsg) tea.Cmd {
	items := a.runsItems()
	if a.runsSel < 0 || a.runsSel >= len(items) {
		return nil
	}
	it := items[a.runsSel]
	switch m.String() {
	case "enter":
		act := a.findActivity(it.ID)
		if act.ID == "" {
			return nil
		}
		return a.roundTripActivityOutput(act)
	case "v":
		a.runsOutput.id = it.ID
		return a.push(viewRunsOutput)
	case "x":
		if a.activity == nil {
			return nil
		}
		_ = a.activity.Kill(it.ID)
	case "t":
		root := it.ProjectRoot
		if root == "" {
			root = a.workdir
		}
		return a.startTriage(root)
	}
	return nil
}

// findActivity resolves one activity by id. The runs panel keeps only ids, not
// full Activity values, so output/kill/send operations look up the current
// registry record.
func (a *App) findActivity(id string) activity.Activity {
	if a.activity == nil {
		return activity.Activity{}
	}
	for _, act := range a.activity.List() {
		if act.ID == id {
			return act
		}
	}
	return activity.Activity{}
}

func (a *App) handleRunsSubagentKey(m tea.KeyMsg) tea.Cmd {
	items := a.runsItems()
	switch m.String() {
	case "enter":
		if a.runsSel >= 0 && a.runsSel < len(items) {
			if a.runsSel == 0 {
				a.threadFilter = ""
			} else {
				a.threadFilter = items[a.runsSel].ID
			}
		}
	case "x":
		if a.runsSel > 0 && a.runsSel < len(items) {
			idx := a.runsSel - 1
			if idx < len(a.subagents) {
				chip := a.subagents[idx]
				if chip.State == "queued" || chip.State == "running" {
					a.cancelSubagent(chip.ID)
				} else {
					a.dismissSubagent(chip.ID)
				}
			}
		}
	}
	return nil
}

// toggleRunsPanel cycles closed ⇄ open+focused. The tab argument selects
// which tab opens when the panel is currently closed; when the panel is
// already open, f9 closes it regardless of focus.
func (a *App) toggleRunsPanel(tab int) {
	if !a.runsOpen {
		if a.height < minRunsPanelOpenHeight {
			return
		}
		a.runsOpen = true
		a.runsFocus = true
		a.runsTab = tab
		a.runsSel = len(a.runsItems()) - 1
		if a.runsSel < 0 {
			a.runsSel = 0
		}
		return
	}
	a.runsOpen = false
	a.runsFocus = false
}

// clamp is a small bounds helper.
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// watchActivityEvents re-arms on the registry's change stream.
func (a *App) watchActivityEvents() tea.Cmd {
	if a.activity == nil {
		return nil
	}
	events := a.activity.Events()
	return func() tea.Msg {
		act, ok := <-events
		if !ok {
			return activityEventMsg{}
		}
		return activityEventMsg(act)
	}
}

// handleActivityEvent updates the runs panel badge and drives the thread
// start/finish lines and the model round-trip. It runs on the Bubble Tea
// goroutine.
func (a *App) handleActivityEvent(m activityEventMsg) tea.Cmd {
	act := activity.Activity(m)
	if act.ID == "" {
		return a.watchActivityEvents()
	}
	var cmd tea.Cmd
	switch act.State {
	case activity.StateRunning:
		if !a.activityAnnounced[act.ID] && !act.Quiet {
			a.activityAnnounced[act.ID] = true
			a.addSystem("▸ " + act.Label + " started — f9 for output")
		}
	case activity.StateDone, activity.StateFailed, activity.StateKilled:
		if !a.activityFinished[act.ID] && !act.Quiet {
			a.activityFinished[act.ID] = true
			a.addSystem(a.activityFinishLine(act))
			if !act.Silent {
				cmd = a.roundTripActivityOutput(act)
			}
		}
	}
	return tea.Batch(cmd, a.watchActivityEvents())
}

func (a *App) activityFinishLine(act activity.Activity) string {
	switch act.State {
	case activity.StateKilled:
		return "■ " + act.Label + " killed"
	case activity.StateFailed:
		return "■ " + act.Label + " failed (exit " + strconv.Itoa(act.ExitCode) + ")"
	default:
		extra := ""
		if len(act.Targets) > 0 {
			extra = fmt.Sprintf(" · %d artifacts · t triage", len(act.Targets))
		}
		return "■ " + act.Label + " done" + extra
	}
}

// Start implements commands.RunObserver / vulnetixcli.RunObserver: it registers
// a Vulnetix subcommand/probe and returns its live-output sink and terminal
// callback. It never mutates App state (it runs on the exec goroutine); the TUI
// reacts to the registry event stream instead.
func (a *App) Start(name string, argv []string, dir string, cancel context.CancelFunc) (func(string), func(int, bool, error)) {
	return a.startActivity(name, argv, dir, cancel, false, false)
}

// startActivity registers an activity with optional silent/quiet flags and
// returns its output callbacks. Quiet suppresses transcript announcements and
// the model round-trip; the activity still appears in the runs panel.
func (a *App) startActivity(name string, argv []string, dir string, cancel context.CancelFunc, silent, quiet bool) (func(string), func(int, bool, error)) {
	if a.activity == nil {
		return func(string) {}, func(int, bool, error) {}
	}
	h := a.activity.Add(activity.Activity{
		Kind:        activity.KindVulnetix,
		Label:       name,
		Argv:        argv,
		Dir:         dir,
		ProjectRoot: dir,
		State:       activity.StateRunning,
		Silent:      silent,
		Quiet:       quiet,
	}, cancel)
	return func(line string) { h.Append(line) }, func(exitCode int, timedOut bool, err error) {
		h.SetTargets(a.vulnetixTargets(dir))
		h.Finish(exitCode, timedOut, err)
	}
}

// quietObserver registers probe subprocesses in the runs panel without
// announcing them or round-tripping their output to the model. The probe's
// argv is fixed and its rendering is harness-composed, so it takes the quiet
// path. No classification exemption is added anywhere.
type quietObserver struct{ a *App }

func (q quietObserver) Start(name string, argv []string, dir string, cancel context.CancelFunc) (func(string), func(int, bool, error)) {
	return q.a.startActivity(name, argv, dir, cancel, false, true)
}

// vulnetixTargets enumerates the artifacts a run produced, excluding signet's
// own state (the same call collectManifest makes).
func (a *App) vulnetixTargets(workdir string) []string {
	if workdir == "" {
		return nil
	}
	arts, err := scanartifacts.Enumerate(config.ProjectDir(workdir))
	if err != nil {
		return nil
	}
	var out []string
	for _, art := range arts {
		if art.Kind == scanartifacts.KindSignet {
			continue
		}
		out = append(out, art.Rel)
	}
	return out
}

// roundTripActivityOutput classifies the captured output and sends it to the
// model exactly like a !shell result: skip the classifier only when the
// guardrails gate is ignored, sanitise always, and seal as a shell attachment.
func (a *App) roundTripActivityOutput(act activity.Activity) tea.Cmd {
	raw := a.activity.Output(act.ID)
	if act.Kind == activity.KindVulnetix {
		// Progress bars and redraws are most of a scan's bytes: they cost
		// tokens and classifier time and tell the model nothing.
		raw = tools.CleanVulnetixOutput(raw)
	}
	body := sanitize.Sanitize(raw)
	pol := a.effectivePosture()
	if pol.Level(posture.ToolResultUnsafe) != posture.Ignore {
		pipe := run.NewPipeline(a.cfg, a.client, a.cache)
		dec, err := pipe.Process(a.ctx, tools.Result{Kind: tools.KindBash, Content: raw})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			a.addSystem(fmt.Sprintf("%s output classified: %s", act.Label, dec.Sentinel.Label()))
			return nil
		}
		body = dec.Content
	}
	label := act.Label
	atts := []run.Attachment{{Kind: "shell", Label: label, Body: body}}
	directive := activityDirective(act)
	if a.working() || a.preSend {
		a.pendingActivitySends = append(a.pendingActivitySends, activitySend{label: label, atts: atts, directive: directive})
		return nil
	}
	return a.sendWithAttachments(fmt.Sprintf("Output of `%s` is attached.", label), atts, directive)
}

// activityDirective states the facts of a finished activity and what to do
// with its output. Without them a model handed "Output of `vulnetix env` is
// attached." spent a minute on Date, Git, LS and Read rediscovering the
// command, its directory and whether it failed, then re-ran the CLI by hand.
// Every field is harness-recorded, never the command's own output.
func activityDirective(act activity.Activity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "`%s` ran as `%s` in %s", act.Label, strings.Join(act.Argv, " "), act.Dir)
	switch {
	case act.State == activity.StateKilled:
		b.WriteString(" and was killed")
	case act.TimedOut:
		b.WriteString(" and timed out")
	case act.ExitCode != 0:
		fmt.Fprintf(&b, " and exited %d", act.ExitCode)
	default:
		b.WriteString(" and exited 0")
	}
	if !act.Started.IsZero() && !act.Ended.IsZero() {
		fmt.Fprintf(&b, " after %s", act.Ended.Sub(act.Started).Round(time.Second))
	}
	b.WriteString(". The user sent its output from the runs panel. ")
	if act.ExitCode != 0 || act.TimedOut || act.State == activity.StateKilled {
		b.WriteString("Diagnose the failure from the attached output first and say what fixes it. ")
	} else {
		b.WriteString("Say what the output shows and whether anything needs doing. ")
	}
	b.WriteString("The command, directory and exit status above are established; do not re-derive them")
	if act.Kind == activity.KindVulnetix {
		b.WriteString(", and re-run or narrow a Vulnetix command only with the Vulnetix tool")
	}
	b.WriteString(".")
	return b.String()
}

// flushPendingActivitySends fires when the turn goes idle, batching several
// finished activities into one turn carrying several attachments.
func (a *App) flushPendingActivitySends() tea.Cmd {
	if a.working() || a.preSend {
		return nil
	}
	// A queued review goes first and on its own: it carries its own
	// objective and fan-out, so the activity outputs wait for the next idle.
	if rs := a.pendingReview; rs != nil {
		a.pendingReview = nil
		return a.sendReview(rs)
	}
	if len(a.pendingActivitySends) == 0 {
		return nil
	}
	sends := a.pendingActivitySends
	a.pendingActivitySends = nil
	var atts []run.Attachment
	var labels, directives []string
	for _, s := range sends {
		atts = append(atts, s.atts...)
		labels = append(labels, s.label)
		if s.directive != "" {
			directives = append(directives, s.directive)
		}
	}
	return a.sendWithAttachments("Output of `"+strings.Join(labels, "`, `")+"` is attached.", atts, strings.Join(directives, "\n"))
}

// startTriage launches the signet:triage-vulns background agent on a project,
// keyed per project so two projects do not collide on the instance name.
func (a *App) startTriage(projectRoot string) tea.Cmd {
	if a.bgManager == nil {
		a.addSystem("triage: no background manager")
		return nil
	}
	profile, err := agentprofile.Load("signet:triage-vulns")
	if err != nil {
		a.addSystem("triage: " + err.Error())
		return nil
	}
	key := "signet:triage-vulns@" + filepath.Base(projectRoot)
	if err := a.bgManager.StartIn(projectRoot, key, profile); err != nil {
		a.addSystem("triage: " + err.Error())
		return nil
	}
	a.registerAgentActivity(key, profile.Name, projectRoot)
	return a.noteAgentStarted(key)
}

// registerAgentActivity registers one background-agent turn in the panel.
func (a *App) registerAgentActivity(key, label, projectRoot string) {
	if a.activity == nil {
		return
	}
	a.activity.Add(activity.Activity{
		Kind:        activity.KindAgent,
		Label:       label,
		Argv:        []string{key},
		Dir:         a.workdir,
		ProjectRoot: projectRoot,
		State:       activity.StateRunning,
	}, func() { _ = a.bgManager.Stop(key) })
}

// registerAgentDesignActivity registers an /agent create design run in the
// panel. It is Silent because the builder's output is model-generated JSON and
// must never be round-tripped back to the model; it is still visible in the
// drawer and readable with f9.
func (a *App) registerAgentDesignActivity(name string, cancel context.CancelFunc) *activity.Handle {
	if a.activity == nil {
		return nil
	}
	return a.activity.Add(activity.Activity{
		Kind:   activity.KindAgent,
		Label:  "agent design: " + name,
		Argv:   []string{"agent", "create", name},
		Dir:    a.workdir,
		State:  activity.StateRunning,
		Silent: true,
	}, cancel)
}
