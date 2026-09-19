package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/activity"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
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
}

// activityEventMsg carries one registry delta into the TUI loop.
type activityEventMsg activity.Activity

// drawerWidth returns the drawer's screen width: the thin rail when closed, the
// wide panel when open.
func (a *App) drawerWidth() int {
	if !a.activityOpen {
		return 3
	}
	content := a.contentWidth()
	w := content * 9 / 10
	if w < 24 {
		w = 24
	}
	if content-w < 12 {
		w = content - 12
	}
	if w < 3 {
		w = 3
	}
	return w
}

// chatWidth is the transcript column width: the outer padding minus the drawer.
func (a *App) chatWidth() int {
	return a.contentWidth() - a.drawerWidth()
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

// handleActivityEvent updates the drawer and drives the thread start/finish
// lines and the model round-trip. It runs on the Bubble Tea goroutine.
func (a *App) handleActivityEvent(m activityEventMsg) tea.Cmd {
	act := activity.Activity(m)
	if act.ID == "" {
		return a.watchActivityEvents()
	}
	var cmd tea.Cmd
	switch act.State {
	case activity.StateRunning:
		if !a.activityAnnounced[act.ID] {
			a.activityAnnounced[act.ID] = true
			a.addSystem("▸ " + act.Label + " started — f9 for output")
		}
	case activity.StateDone, activity.StateFailed, activity.StateKilled:
		if !a.activityFinished[act.ID] {
			a.activityFinished[act.ID] = true
			a.addSystem(a.activityFinishLine(act))
			cmd = a.roundTripActivityOutput(act)
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
	}, cancel)
	return func(line string) { h.Append(line) }, func(exitCode int, timedOut bool, err error) {
		h.SetTargets(a.vulnetixTargets(dir))
		h.Finish(exitCode, timedOut, err)
	}
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
	body := sanitize.Sanitize(raw)
	pol := a.effectivePosture()
	if pol.Level(posture.ToolResultUnsafe) != posture.Ignore {
		pipe := run.NewPipeline(a.cfg, a.client, a.cache)
		dec, err := pipe.Process(a.ctx, tools.Result{Kind: tools.KindBash, Content: raw})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			a.addSystem(fmt.Sprintf("%s output classified %s and not sent", act.Label, dec.Sentinel))
			return nil
		}
		body = dec.Content
	}
	label := act.Label
	atts := []run.Attachment{{Kind: "shell", Label: label, Body: body}}
	if a.working() || a.preSend {
		a.pendingActivitySends = append(a.pendingActivitySends, activitySend{label: label, atts: atts})
		return nil
	}
	return a.sendWithAttachments(fmt.Sprintf("Output of `%s` is attached.", label), atts, "")
}

// flushPendingActivitySends fires when the turn goes idle, batching several
// finished activities into one turn carrying several attachments.
func (a *App) flushPendingActivitySends() tea.Cmd {
	if len(a.pendingActivitySends) == 0 || a.working() || a.preSend {
		return nil
	}
	sends := a.pendingActivitySends
	a.pendingActivitySends = nil
	var atts []run.Attachment
	var labels []string
	for _, s := range sends {
		atts = append(atts, s.atts...)
		labels = append(labels, s.label)
	}
	return a.sendWithAttachments("Output of `"+strings.Join(labels, "`, `")+"` is attached.", atts, "")
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
	a.addSystem("triage started for " + projectRoot)
	return a.watchAgentEvents(key)
}

// registerAgentActivity registers one background-agent turn in the drawer.
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

// registerShellActivity registers one !shell command in the drawer.
func (a *App) registerShellActivity(callID, command, workdir string) {
	if a.activity == nil {
		return
	}
	a.activity.Add(activity.Activity{
		ID:    callID,
		Kind:  activity.KindShell,
		Label: "!" + command,
		Argv:  []string{"!" + command},
		Dir:   workdir,
		State: activity.StateRunning,
	}, func() {})
}

// toggleActivityDrawer cycles closed → open+focused → closed.
func (a *App) toggleActivityDrawer() {
	if !a.activityOpen {
		a.activityOpen = true
		a.activityFocus = true
		a.stripFocus = false
		a.activitySel = len(a.activity.List()) - 1
		return
	}
	if a.activityFocus {
		a.activityFocus = false
		return
	}
	a.activityOpen = false
	a.activityFocus = false
}

// handleActivityKey routes keys while the drawer owns focus.
func (a *App) handleActivityKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "esc":
		a.activityFocus = false
		return nil
	case "up", "k":
		if a.activitySel > 0 {
			a.activitySel--
		}
	case "down", "j":
		if a.activitySel < len(a.activity.List())-1 {
			a.activitySel++
		}
	case "pgup":
		a.activityVP.PageUp()
	case "pgdown":
		a.activityVP.PageDown()
	case "x":
		acts := a.activity.List()
		if a.activitySel >= 0 && a.activitySel < len(acts) {
			_ = a.activity.Kill(acts[a.activitySel].ID)
		}
	case "t":
		acts := a.activity.List()
		if a.activitySel >= 0 && a.activitySel < len(acts) {
			root := acts[a.activitySel].ProjectRoot
			if root == "" {
				root = a.workdir
			}
			return a.startTriage(root)
		}
	case "enter":
		acts := a.activity.List()
		if a.activitySel >= 0 && a.activitySel < len(acts) {
			return a.roundTripActivityOutput(acts[a.activitySel])
		}
	}
	return nil
}

// renderActivityRail renders the thin closed rail.
func (a *App) renderActivityRail() string {
	running := 0
	for _, act := range a.activity.List() {
		if act.State == activity.StateQueued || act.State == activity.StateRunning {
			running++
		}
	}
	var colour lipgloss.TerminalColor = components.ColorMuted
	glyph := "│"
	if running > 0 {
		colour = components.ColorTeal
		glyph = "▸"
	}
	return lipgloss.NewStyle().Foreground(colour).Render(glyph)
}

// renderActivityDrawer renders the open drawer: a list of activities over a
// viewport of the selected activity's output.
func (a *App) renderActivityDrawer() string {
	acts := a.activity.List()
	var b strings.Builder
	b.WriteString(components.SectionHeader("activity", "f9 close", a.drawerWidth()))
	b.WriteString("\n")
	for i, act := range acts {
		marker := "  "
		if i == a.activitySel {
			marker = "▸ "
		}
		line := fmt.Sprintf("%s%s · %s", marker, act.Label, act.State)
		if act.State == activity.StateDone || act.State == activity.StateFailed || act.State == activity.StateKilled {
			if act.State == activity.StateDone && len(act.Targets) > 0 {
				line += fmt.Sprintf(" · %d artifacts", len(act.Targets))
			} else if act.ExitCode != 0 {
				line += fmt.Sprintf(" · exit %d", act.ExitCode)
			}
		}
		if i == a.activitySel {
			line = components.AccentStyle.Render(line)
		} else {
			line = components.MutedStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString(components.Rule(a.drawerWidth()))
	b.WriteString("\n")

	output := ""
	if a.activitySel >= 0 && a.activitySel < len(acts) {
		output = a.activity.Output(acts[a.activitySel].ID)
	}
	a.activityVP.Width = a.drawerWidth()
	if a.activityVP.Width < 8 {
		a.activityVP.Width = 8
	}
	a.activityVP.Height = a.height - 8
	if a.activityVP.Height < 5 {
		a.activityVP.Height = 5
	}
	a.activityVP.SetContent(components.MutedStyle.Render(output))
	b.WriteString(a.activityVP.View())
	return b.String()
}
