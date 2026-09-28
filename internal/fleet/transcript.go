package fleet

import (
	"fmt"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/turnlog"
)

// transcript writes a worker item's session, keyed by the trusted
// repository — not the worktree — so it lists with the project's other
// sessions, resumes, and reaches ReadSession and SearchSessions.
type transcript struct{ log *turnlog.Log }

func (w *Worker) transcript(t Turn) *transcript {
	if w.Sessions == nil {
		return &transcript{log: turnlog.New(nil)}
	}
	key, err := session.KeyFor(w.Repo)
	if err != nil {
		return &transcript{log: turnlog.New(nil)}
	}
	sw, err := session.NewWriter(w.Sessions, key, t.SessionID, session.Meta{
		Cwd: t.Workdir, OriginCwd: w.Repo, ActiveProfile: w.Profile.Name, Mode: "goal",
	})
	if err != nil {
		w.logf("transcript: %v", err)
		return &transcript{log: turnlog.New(nil)}
	}
	sw.Name(fmt.Sprintf("%s · %s %s", w.Profile.Name, t.Item.Short(), kanban.CleanTitle(t.Item.Title)))
	return &transcript{log: turnlog.New(sw)}
}

func (t *transcript) user(text string) { t.log.User(text, nil) }

func (t *transcript) observe(e agent.Event) { t.log.Observe(e) }

func (t *transcript) finish(res run.Result, err error) {
	t.log.Flush()
	switch {
	case err != nil:
		t.log.System("worker turn ended: " + err.Error())
	default:
		t.log.System(fmt.Sprintf("goal %s after %d passes", res.StopReason, res.Passes))
	}
}
