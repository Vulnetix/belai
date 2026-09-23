// Recovery subagent for supervised processes. The subagent sees a read-only
// tool surface plus SubAgentLog and ProcessRestart. ProcessRestart re-runs a
// command the user already authorised by typing `!!`; argv[0] is pinned, and
// each restart consumes one recovery attempt.
package bgproc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/repoindex"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tools"
)

// runRecovery dispatches the recovery subagent for an unexpectedly exited
// process.
func (m *Manager) runRecovery(id string) {
	p, ok := m.Lookup(id)
	if !ok {
		return
	}

	ctx := calltrace.WithSession(context.Background(), m.sessionID())
	tail := m.tail(id, 200)
	body := m.filterTail(ctx, tail)

	workdir := p.Dir
	if workdir == "" {
		workdir = m.workdir
	}

	max := m.settings.Resilience.MaxProcessRecoveriesOr(3)
	instr := fmt.Sprintf(
		"A supervised process exited unexpectedly and may need to be restarted.\n"+
			"- process handle: %s\n"+
			"- command: %s\n"+
			"- working directory: %s\n"+
			"- exit code: %d\n"+
			"- attempts so far: %d / %d\n"+
			"- tail is attached as process: %s\n\n"+
			"Diagnose using SubAgentLog if needed, then call ProcessRestart with the same process handle. "+
			"If you change flags, keep argv[0] identical to the original command. "+
			"If the failure is unrecoverable, explain why; do not ask the user.",
		id, p.Command, workdir, p.ExitCode, p.Attempts, max, p.Command,
	)

	perms := permissions.From(m.settings.Permissions.Allow, m.settings.Permissions.Ask, m.settings.Permissions.Deny)
	ix := repoindex.Scan(ctx, workdir)
	caps := m.caps
	if caps.IsEmpty() {
		caps = tools.DetectDefault()
	}

	reg := tools.DefaultWithCaps(workdir, true, caps, ix).Plan().
		With(&tools.SubAgentLog{Logs: m}, &tools.ProcessRestart{Ctl: m})

	sess, err := agent.NewSession(agent.Options{
		Cfg:           m.cfg,
		Client:        m.client,
		Registry:      reg,
		Live:          m.live,
		PlanMode:      false,
		AllowExplore:  false,
		AllowClarify:  false,
		AllowAsk:      false,
		AskDisabled:   true,
		AllowPassLoop: false,
		MaxIterations: m.settings.Resilience.MaxExploreIterationsOr(8),
		Workdir:       workdir,
		Settings:      m.settings,
		Perms:         perms,
		Caps:          caps,
		RepoIndex:     ix,
		SkipNonceSeed: true,
	})
	if err != nil {
		m.pushEvent(Event{ID: id, Kind: "error", Err: fmt.Errorf("build recovery session: %w", err)})
		return
	}

	ch := sess.RunStream(ctx, nil, agent.TurnInput{
		Prompt:      instr,
		Attachments: []run.Attachment{{Kind: "process", Label: p.Command, Body: body}},
	})
	var result run.Result
	for e := range ch {
		// Forward recovery activity as process events so the UI can render it.
		switch e.Kind {
		case agent.EventToolStartKind:
			m.pushEvent(Event{ID: id, Kind: "recover", Text: fmt.Sprintf("→ %s", e.Tool.Name)})
		case agent.EventToolResultKind:
			m.pushEvent(Event{ID: id, Kind: "recover", Text: fmt.Sprintf("← %s: %s", e.ToolName, truncate(e.ToolResult, 200))})
		case agent.EventDoneKind:
			result = e.Result
		case agent.EventErrorKind:
			m.pushEvent(Event{ID: id, Kind: "error", Err: e.Err})
		}
	}

	// Success is measured by whether the process is still alive 10 seconds
	// after the subagent's turn.
	time.Sleep(recoveryAliveCheck)
	m.mu.RLock()
	p2, ok := m.procs[id]
	alive := ok && p2.state == StateRunning
	m.mu.RUnlock()

	if alive {
		m.pushEvent(Event{ID: id, Kind: "restart", Process: p, Text: fmt.Sprintf("recovery succeeded: %s", result.Reply)})
	} else {
		m.pushEvent(Event{ID: id, Kind: "recover", Text: fmt.Sprintf("recovery did not keep process running: %s", result.Reply)})
	}
}

// tail returns the last n lines of a process's captured output from its ring
// buffer. It is the untrusted part of the recovery input.
func (m *Manager) tail(id string, n int) string {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return ""
	}
	lines := p.tail.get()
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// filterTail classifies the process tail when guardrails are enabled and
// returns either the classifier's SAFE text or the empty string. An empty
// body means recovery proceeds from the harness-owned facts alone.
func (m *Manager) filterTail(ctx context.Context, tail string) string {
	if tail == "" {
		return ""
	}
	if m.live.Level(posture.ToolResultUnsafe) == posture.Ignore {
		sanitized := sanitize.Sanitize(tail)
		if len(sanitized) > 16*1024 {
			return sanitized[len(sanitized)-16*1024:]
		}
		return sanitized
	}
	pipe := run.NewPipeline(m.cfg, m.client, nil)
	dec, err := pipe.Process(ctx, tools.Result{Kind: tools.KindProcess, Content: tail})
	if err != nil || dec.Action != rolemanager.ActionProceed {
		return ""
	}
	if len(dec.Content) > 16*1024 {
		return dec.Content[len(dec.Content)-16*1024:]
	}
	return dec.Content
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
