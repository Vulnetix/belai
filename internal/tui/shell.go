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
	"github.com/vulnetix/signet/internal/tools"
)

// shellStartedMsg updates the UI when a shell command begins executing.
type shellStartedMsg struct{ command string }

// shellDoneMsg carries the result of an async shell execution.
type shellDoneMsg struct {
	command  string
	body     string
	err      error
	sentinel rolemanager.Sentinel
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
	pol := a.posture

	return func() tea.Msg {
		bash, ok := tools.Default(workdir).Find("Bash")
		if !ok {
			return shellDoneMsg{command: cmd, err: fmt.Errorf("Bash tool not registered")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := bash.Execute(ctx, map[string]any{"command": cmd})
		if err != nil {
			return shellDoneMsg{command: cmd, err: err}
		}
		body := res.Content
		pipe := rolemanager.NewPipeline(run.NewClassifier(cfg, client))
		dec, perr := pipe.Process(res)
		if perr == nil && dec.Action == rolemanager.ActionProceed {
			body = dec.Content
		}
		if perr == nil && dec.Action != rolemanager.ActionProceed && pol.Level(posture.ToolResultUnsafe) == posture.Ignore {
			body = dec.Content
			dec.Action = rolemanager.ActionProceed
		}
		return shellDoneMsg{command: cmd, body: body, sentinel: dec.Sentinel, err: perr}
	}
}

// handleShellDone routes the classified shell output into the transcript and,
// when safe, back to the model under the debug profile.
func (a *App) handleShellDone(m shellDoneMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem(fmt.Sprintf("!%s failed: %v", m.command, m.err))
		return nil
	}

	// Show the command output locally first (truncated for display).
	preview := m.body
	const maxPreview = 512
	if len(preview) > maxPreview {
		preview = preview[:maxPreview] + "…"
	}
	a.addSystem(fmt.Sprintf("$ %s\n%s", m.command, preview))

	if m.sentinel.IsSafe() || a.posture.Level(posture.ToolResultUnsafe) == posture.Ignore {
		input := fmt.Sprintf("Output of `%s` is attached.", m.command)
		return a.sendWithAttachments(input, []run.Attachment{
			{Kind: "shell", Label: m.command, Body: m.body},
		})
	}

	a.addSystem(fmt.Sprintf("shell output classified %s and not sent", m.sentinel))
	return nil
}

func isShellInput(s string) bool { return strings.HasPrefix(s, "!") }
