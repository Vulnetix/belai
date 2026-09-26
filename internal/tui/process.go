// Supervised-process composer and event handling. `!!cmd` starts a process
// that lives as long as Belai; its output streams to a log and the UI, and
// if it exits unexpectedly a recovery subagent is dispatched.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/activity"
	"github.com/vulnetix/belai/internal/bgproc"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/processlib"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/tui/components"
)

// processEventMsg carries one supervised-process lifecycle event.
type processEventMsg bgproc.Event

// processProgressMsg carries one batch of live supervised-process output.
// It mirrors shellProgressMsg so the same append/re-arm pattern works.
type processProgressMsg struct {
	id   string
	text string
	done bool
}

func isProcessInput(s string) bool { return strings.HasPrefix(s, "!!") }

// handleProcess starts a supervised process from the composer. It persists an
// entry in the project process library and returns a live tool row. No model
// is contacted while the process runs.
func (a *App) handleProcess(input string) tea.Cmd {
	cmd := strings.TrimSpace(strings.TrimPrefix(input, "!!"))
	if cmd == "" {
		return nil
	}
	if !a.processAllowed(cmd) {
		return nil
	}

	entry, err := processlib.CreateUnique(config.ScopeProject, a.workdir, cmd)
	if err != nil {
		a.addSystem(fmt.Sprintf("process library: %v", err))
		return nil
	}

	proc, watch, ok := a.startSupervised(entry.Name, cmd)
	if !ok {
		return nil
	}
	a.addSystem(fmt.Sprintf("process %s started: %s", proc.ID, cmd))
	return watch
}

// processAllowed applies the plan-mode surface to a process command, the same
// gate as a Bash call, and reports a refusal in the transcript.
func (a *App) processAllowed(cmd string) bool {
	perms := permissions.From(a.settings.Permissions.Allow, a.settings.Permissions.Ask, a.settings.Permissions.Deny)
	surface := tools.PlanSurface{GuardrailsOff: !a.guardrailsEnabled(), Perms: perms}
	if !modes.ToolAllowed("bash", map[string]any{"command": cmd}, a.mode == "plan", surface) {
		a.addSystem("process command not allowed in plan mode: " + cmd)
		return false
	}
	return true
}

// startSupervised launches a library process under the manager and adds its
// live tool row and runs-panel activity. Every start path goes through the
// plan-mode gate here, so a library entry cannot start what `!!` could not.
func (a *App) startSupervised(name, cmd string) (bgproc.Process, tea.Cmd, bool) {
	if !a.processAllowed(cmd) {
		return bgproc.Process{}, nil, false
	}
	proc, err := a.procManager.Start(name, cmd)
	if err != nil {
		a.addSystem(fmt.Sprintf("process start failed: %v", err))
		return bgproc.Process{}, nil, false
	}

	callID := "proc-" + proc.ID
	a.messages = append(a.messages, components.Message{
		Role:       "tool",
		ToolName:   "Process",
		ToolArgs:   toolArgsString(map[string]any{"command": cmd}),
		ToolCallID: callID,
		StartedAt:  time.Now(),
	})
	a.follow = true
	a.registerProcessActivity(callID, cmd, a.workdir)
	return proc, a.watchProcessEvents(), true
}

// handleProcessProgress appends live output to the running process row and
// to the activity registry so the F9 runs panel can open the full log.
func (a *App) handleProcessProgress(m processProgressMsg) tea.Cmd {
	if m.done {
		return nil
	}
	callID := "proc-" + m.id
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == callID {
			a.messages[i].AppendProgress(m.text)
			break
		}
	}
	if a.activity != nil {
		a.activity.Append(callID, m.text)
	}
	if a.follow {
		a.vp.GotoBottom()
	}
	return a.watchProcessEvents()
}

// handleProcessEvent routes process lifecycle events to the transcript and
// updates the live row. It always re-arms the watcher.
func (a *App) handleProcessEvent(m processEventMsg) tea.Cmd {
	switch m.Kind {
	case "progress":
		return a.handleProcessProgress(processProgressMsg{id: m.ID, text: m.Text})
	case "start":
		// Row was already added by handleProcess.
	case "exit", "stop", "fail":
		callID := "proc-" + m.ID
		if a.activity != nil {
			code := 0
			timedOut := false
			if m.Err != nil {
				code = m.Process.ExitCode
			}
			a.activity.Finish(callID, code, timedOut, m.Err)
		}
		for i := len(a.messages) - 1; i >= 0; i-- {
			if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == callID {
				a.messages[i].SetContent(m.Process.State.Label())
				a.messages[i].Status = toolResultStatus("Process", m.Process.State.Label())
				break
			}
		}
		if m.Kind == "fail" {
			a.addSystem(fmt.Sprintf("process %s failed after %d recovery attempts", m.ID, m.Process.Attempts))
		}
	case "restart":
		a.addSystem(fmt.Sprintf("process %s restarted", m.ID))
	case "recover":
		// Recovery subagent activity is already reflected by its own tool
		// rows in the main transcript; keep the process row live.
	}
	return a.watchProcessEvents()
}

func (a *App) watchProcessEvents() tea.Cmd {
	return func() tea.Msg {
		e, ok := <-a.procManager.Events()
		if !ok {
			return processEventMsg{Kind: "closed"}
		}
		return processEventMsg(e)
	}
}

// registerProcessActivity adds the supervised process to the honest runs
// panel.
// autoStartProcesses loads the merged enabled process library and starts
// any entries that are not already running (the lock file handles races with
// another Belai instance).
func (a *App) autoStartProcesses() {
	if a.procManager == nil {
		return
	}
	global, _ := processlib.Load(config.ScopeGlobal, a.workdir)
	project, _ := processlib.Load(config.ScopeProject, a.workdir)
	for _, e := range processlib.Enabled(processlib.Merge(global.Entries, project.Entries)) {
		if _, err := a.procManager.Start(e.Name, e.Command); err != nil {
			// Already running or lock conflict; keep going.
			continue
		}
	}
}

func (a *App) registerProcessActivity(callID, command, workdir string) {
	if a.activity == nil {
		return
	}
	a.activity.Add(activity.Activity{
		ID:    callID,
		Kind:  activity.KindProcess,
		Label: "!!" + command,
		Argv:  []string{"!!" + command},
		Dir:   workdir,
		State: activity.StateRunning,
		Quiet: true,
	}, func() {
		// Cancel callback: extract process id from callID ("proc-<id>").
		id := strings.TrimPrefix(callID, "proc-")
		_ = a.procManager.Stop(id)
	})
}
