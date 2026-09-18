package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/tui/components"
)

// shellDoneMsg carries the result of an async shell execution.
type shellDoneMsg struct {
	command  string
	callID   string
	body     string
	err      error
	sentinel rolemanager.Sentinel
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

// handleShellProgress appends live output to the row for a running command.
func (a *App) handleShellProgress(m shellProgressMsg) tea.Cmd {
	if m.done {
		return nil
	}
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == m.callID {
			a.messages[i].AppendProgress(m.text)
			break
		}
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

	if !modes.ToolAllowed("bash", map[string]any{"command": cmd}, a.mode == "plan") {
		a.addSystem("shell command not allowed in plan mode: " + cmd)
		return nil
	}

	cfg := a.cfg
	client := a.client
	workdir := a.workdir
	bashReadOnly := a.settings.ReadOnlyEnabled()
	pol := a.effectivePosture()

	// Render `!cmd` as a real tool row rather than a system notice. It then
	// gets the same live tail, tail-anchored preview and ctrl+o expansion as a
	// command the agent runs, instead of a second, divergent presentation of
	// the same thing.
	callID := fmt.Sprintf("shell-%d", time.Now().UnixNano())
	a.messages = append(a.messages, components.Message{
		Role:       "tool",
		ToolName:   "Bash",
		ToolArgs:   toolArgsString(map[string]any{"command": cmd}),
		ToolCallID: callID,
		StartedAt:  time.Now(),
	})
	a.follow = true

	// Buffered so a short command does not block on a UI that has not armed
	// its watcher yet; beyond that, a full channel throttles the subprocess,
	// which is the intended backpressure.
	progress := make(chan tools.Progress, 64)

	exec := func() tea.Msg {
		defer close(progress)

		bash, ok := tools.Default(workdir, bashReadOnly).Find("Bash")
		if !ok {
			return shellDoneMsg{command: cmd, callID: callID, err: fmt.Errorf("Bash tool not registered")}
		}
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
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
			return shellDoneMsg{command: cmd, callID: callID, body: sanitize.Sanitize(res.Content), sentinel: rolemanager.SentinelSafe}
		}
		body := res.Content
		pipe := run.NewPipeline(cfg, client, a.cache)
		dec, perr := pipe.Process(ctx, res)
		if perr == nil && dec.Action == rolemanager.ActionProceed {
			body = dec.Content
		}
		return shellDoneMsg{command: cmd, callID: callID, body: body, sentinel: dec.Sentinel, err: perr}
	}

	return tea.Batch(exec, watchShellProgress(callID, progress))
}

// handleShellDone routes the classified shell output into the transcript and,
// when safe, back to the model under the debug profile.
func (a *App) handleShellDone(m shellDoneMsg) tea.Cmd {
	if m.err != nil {
		a.setShellResult(m.callID, fmt.Sprintf("failed: %v", m.err), "✗")
		return nil
	}

	// The output lands on the command's own tool row, which collapses and
	// expands like any other. There is no display-side truncation here: the
	// row's preview policy decides how much to show.
	a.setShellResult(m.callID, m.body, toolResultStatus("Bash", m.body))

	if m.sentinel.IsSafe() || a.effectivePosture().Level(posture.ToolResultUnsafe) == posture.Ignore {
		input := fmt.Sprintf("Output of `%s` is attached.", m.command)
		return a.sendWithAttachments(input, []run.Attachment{
			{Kind: "shell", Label: m.command, Body: m.body},
		})
	}

	a.addSystem(fmt.Sprintf("shell output classified %s and not sent", m.sentinel))
	return nil
}

// setShellResult lands a finished `!cmd` on its own tool row, replacing the
// live tail. A row is always present — handleShell appends it before running —
// but fall back to a system notice rather than losing the output if it is not.
func (a *App) setShellResult(callID, body, status string) {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "tool" && a.messages[i].ToolCallID == callID {
			a.messages[i].SetContent(body)
			a.messages[i].Status = status
			return
		}
	}
	a.addSystem(body)
}

func isShellInput(s string) bool { return strings.HasPrefix(s, "!") }
