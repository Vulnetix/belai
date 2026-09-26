package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/tui/components"
)

// shellDoneMsg carries the result of an async shell execution. raw is what the
// command printed, shown in its panel; body is the sanitised, classified copy
// that is the only thing ever sent to the model.
type shellDoneMsg struct {
	command  string
	callID   string
	raw      string
	body     string
	err      error
	sentinel rolemanager.Sentinel
	// send is set when body may go to the model: guardrails off, or a clean
	// verdict with no classifier error.
	send bool
}

// shellProgressMsg carries one batch of live output from a running `!cmd`.
// ch is handed back so the watcher can re-arm without the App holding
// per-command state.
type shellProgressMsg struct {
	callID string
	text   string
	ch     chan tools.Progress
	done   bool
}

// watchShellProgress blocks on the next batch of shell output, mirroring how
// nextAgent pumps the agent's event channel.
func watchShellProgress(callID string, ch chan tools.Progress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return shellProgressMsg{callID: callID, done: true}
		}
		return shellProgressMsg{callID: callID, text: p.Text, ch: ch}
	}
}

// handleShellProgress appends live output to the panel for a running command.
func (a *App) handleShellProgress(m shellProgressMsg) tea.Cmd {
	if m.done {
		return nil
	}
	if i := a.shellRow(m.callID); i >= 0 {
		a.messages[i].AppendProgress(m.text)
	}
	if a.follow {
		a.vp.GotoBottom()
	}
	return watchShellProgress(m.callID, m.ch)
}

// handleShell executes one local command and immediately round-trips its
// output to the model with the builtin debug profile engaged. The output is
// classified before sealing; under enforce a non-SAFE sentinel is shown locally
// and not sent.
func (a *App) handleShell(input string) tea.Cmd {
	cmd := strings.TrimSpace(strings.TrimPrefix(input, "!"))
	if cmd == "" {
		return nil
	}

	perms := permissions.From(a.settings.Permissions.Allow, a.settings.Permissions.Ask, a.settings.Permissions.Deny)
	surface := tools.PlanSurface{GuardrailsOff: !a.guardrailsEnabled(), Perms: perms}
	if !modes.ToolAllowed("bash", map[string]any{"command": cmd}, a.mode == "plan", surface) {
		a.addSystem("shell command not allowed in plan mode: " + cmd)
		return nil
	}

	cfg := a.cfg
	client := a.client
	workdir := a.workdir
	bashReadOnly := a.settings.ReadOnlyEnabled()
	pol := a.effectivePosture()

	// A `!cmd` gets its own shell panel: the raw output, a live tail while it
	// runs, and ctrl+o / ctrl+c like any other panel. It is not a tool row —
	// the model did not ask for it — and not a runs-panel activity, whose
	// finish would round-trip the output a second time.
	callID := fmt.Sprintf("shell-%d", time.Now().UnixNano())
	a.messages = append(a.messages, components.Message{
		Role:       components.ShellRole,
		ToolArgs:   components.ShellArgs(cmd),
		ToolCallID: callID,
		StartedAt:  time.Now(),
		CreatedAt:  time.Now(),
	})
	a.follow = true

	// Buffered so a short command does not block on a UI that has not armed
	// its watcher yet; beyond that, a full channel throttles the subprocess,
	// which is the intended backpressure.
	progress := make(chan tools.Progress, 64)
	traceCtx := a.toolContext(a.ctx, "Bash", callID)
	// The inline shell runs under the same OS sandbox as the agent's Bash.
	traceCtx = sandbox.WithPolicy(traceCtx, sandbox.FromSettings(a.settings.Sandbox, append([]string{workdir}, a.workspaceDirs...), a.effectivePosture()))

	exec := func() tea.Msg {
		defer close(progress)

		bash, ok := tools.Default(workdir, bashReadOnly).Find("Bash")
		if !ok {
			return shellDoneMsg{command: cmd, callID: callID, err: fmt.Errorf("Bash tool not registered")}
		}
		ctx, cancel := context.WithTimeout(traceCtx, 30*time.Second)
		defer cancel()

		var res tools.Result
		var err error
		if st, ok := bash.(tools.StreamingTool); ok {
			res, err = st.ExecuteStream(ctx, map[string]any{"command": cmd}, func(p tools.Progress) {
				progress <- p
			})
		} else {
			res, err = bash.Execute(ctx, map[string]any{"command": cmd})
		}
		if err != nil {
			return shellDoneMsg{command: cmd, callID: callID, err: err}
		}
		// With the gate ignored the verdict cannot change the outcome, so the
		// classifier is not called at all. Calling it and then discarding the
		// answer would spend a round trip per `!cmd` and send the command's
		// output to the provider's classifier turn, which is the opposite of
		// what turning guardrails off asks for. Sanitising still runs.
		if pol.Level(posture.ToolResultUnsafe) == posture.Ignore {
			return shellDoneMsg{command: cmd, callID: callID, raw: res.Content, body: sanitize.Sanitize(res.Content), sentinel: rolemanager.SentinelSafe, send: true}
		}
		// The sanitised copy is the fallback, never the raw output: raw is for
		// the panel only.
		body := sanitize.Sanitize(res.Content)
		pipe := run.NewPipeline(cfg, client, a.cache)
		dec, perr := pipe.Process(ctx, res)
		if perr == nil && dec.Action == rolemanager.ActionProceed {
			body = dec.Content
		}
		return shellDoneMsg{
			command:  cmd,
			callID:   callID,
			raw:      res.Content,
			body:     body,
			sentinel: dec.Sentinel,
			err:      perr,
			send:     perr == nil && dec.Sentinel.IsSafe(),
		}
	}

	return tea.Batch(exec, watchShellProgress(callID, progress))
}

// handleShellDone lands the raw output in the command's shell panel and, when
// the classified copy is safe, sends that copy to the model under the debug
// profile. The panel always shows what the command printed; only the
// attachment depends on the verdict.
func (a *App) handleShellDone(m shellDoneMsg) tea.Cmd {
	switch {
	case m.err != nil && m.raw == "":
		a.setShellResult(m.callID, fmt.Sprintf("failed: %v", m.err), "✗")
		return nil
	case m.err != nil:
		// The command ran; classifying its output did not. Show the output and
		// send nothing — an unclassified result never reaches the model.
		a.setShellResult(m.callID, m.raw, "not sent: classifier failed")
		a.addSystem(fmt.Sprintf("shell output not sent: %v", m.err))
		return nil
	case !m.send:
		a.setShellResult(m.callID, m.raw, "not sent: "+m.sentinel.Label())
		a.addSystem(fmt.Sprintf("shell output classified: %s", m.sentinel.Label()))
		return nil
	}

	a.setShellResult(m.callID, m.raw, toolResultStatus("Bash", m.raw))
	input := fmt.Sprintf("Output of `%s` is attached.", m.command)
	return a.sendWithAttachments(input, []run.Attachment{
		{Kind: "shell", Label: m.command, Body: m.body},
	}, "")
}

// setShellResult lands a finished `!cmd` in its shell panel, replacing the
// live tail. A panel is always present — handleShell appends it before running
// — but fall back to a system notice rather than losing the output if it is
// not.
func (a *App) setShellResult(callID, raw, status string) {
	if i := a.shellRow(callID); i >= 0 {
		a.messages[i].SetContent(raw)
		a.messages[i].Status = status
		if !a.messages[i].StartedAt.IsZero() {
			a.messages[i].DurationMS = time.Since(a.messages[i].StartedAt).Milliseconds()
		}
		return
	}
	a.addSystem(raw)
}

// shellRow returns the index of the shell panel for callID, or -1.
func (a *App) shellRow(callID string) int {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == components.ShellRole && a.messages[i].ToolCallID == callID {
			return i
		}
	}
	return -1
}

func isShellInput(s string) bool { return strings.HasPrefix(s, "!") }
