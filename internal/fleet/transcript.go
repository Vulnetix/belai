package fleet

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
)

// transcript writes a worker item's session, keyed by the trusted
// repository — not the worktree — so it lists with the project's other
// sessions, resumes, and reaches ReadSession and SearchSessions.
type transcript struct {
	w *session.Writer

	mu    sync.Mutex
	text  strings.Builder
	calls []map[string]any
}

func (w *Worker) transcript(t Turn) *transcript {
	if w.Sessions == nil {
		return &transcript{}
	}
	key, err := session.KeyFor(w.Repo)
	if err != nil {
		return &transcript{}
	}
	sw, err := session.NewWriter(w.Sessions, key, t.SessionID, session.Meta{
		Cwd: t.Workdir, OriginCwd: w.Repo, ActiveProfile: w.Profile.Name, Mode: "goal",
	})
	if err != nil {
		w.logf("transcript: %v", err)
		return &transcript{}
	}
	sw.Name(fmt.Sprintf("%s · %s %s", w.Profile.Name, t.Item.Short(), kanban.CleanTitle(t.Item.Title)))
	return &transcript{w: sw}
}

func (t *transcript) user(text string) {
	if t.w != nil {
		t.w.User(text)
	}
}

// flush writes the assistant text and calls gathered so far.
func (t *transcript) flush() {
	if t.w == nil {
		return
	}
	t.mu.Lock()
	text, calls := t.text.String(), t.calls
	t.text.Reset()
	t.calls = nil
	t.mu.Unlock()
	if strings.TrimSpace(text) != "" || len(calls) > 0 {
		t.w.Assistant(text, calls, nil)
	}
}

func (t *transcript) observe(e agent.Event) {
	if t.w == nil {
		return
	}
	switch e.Kind {
	case agent.EventTextKind:
		t.mu.Lock()
		t.text.WriteString(e.Text)
		t.mu.Unlock()
	case agent.EventToolStartKind:
		if e.Tool == nil {
			return
		}
		args := e.Tool.RawArgs
		if args == "" && len(e.Tool.Args) > 0 {
			if b, err := json.Marshal(e.Tool.Args); err == nil {
				args = string(b)
			}
		}
		t.mu.Lock()
		t.calls = append(t.calls, map[string]any{"id": e.Tool.ID, "name": e.Tool.Name, "args": args})
		t.mu.Unlock()
	case agent.EventToolResultKind:
		t.flush()
		t.w.Tool(e.ToolCallID, e.ToolName, e.ToolArgs, "done", e.ToolResult)
	case agent.EventWarningKind:
		if e.Warning != "" {
			t.w.System(e.Warning)
		}
	}
}

func (t *transcript) finish(res run.Result, err error) {
	if t.w == nil {
		return
	}
	t.flush()
	switch {
	case err != nil:
		t.w.System("worker turn ended: " + err.Error())
	default:
		t.w.System(fmt.Sprintf("goal %s after %d passes", res.StopReason, res.Passes))
	}
}
