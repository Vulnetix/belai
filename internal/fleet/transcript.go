package fleet

import (
	"fmt"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
)

// transcript writes a worker item's session, keyed by the trusted
// repository — not the worktree — so it lists with the project's other
// sessions, resumes, and reaches ReadSession and SearchSessions.
type transcript struct {
	log    *turnlog.Log
	mirror *sessionsync.Syncer
	// detach stops the role-manager record sink; a worker is its own process,
	// so the process-wide sink belongs to this one transcript.
	detach func()
}

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
	name := fmt.Sprintf("%s · %s %s", w.Profile.Name, t.Item.Short(), kanban.CleanTitle(t.Item.Title))
	sw.Name(name)
	if w.mirror != nil {
		w.mirror.Activate(sessionsync.SessionInfo{
			ID: t.SessionID, Path: w.Sessions.SessionPath(key, t.SessionID), ProjectKey: string(key),
			ProjectName: key.Project(), Cwd: t.Workdir, Name: name,
			Model: w.Cfg.Model, Provider: w.Cfg.Provider, Mode: "goal",
		})
	}
	log := turnlog.New(sw)
	return &transcript{log: log, mirror: w.mirror, detach: log.AttachRoleManager()}
}

func (t *transcript) user(text string) { t.log.User(text, nil) }

func (t *transcript) observe(e agent.Event) { t.log.Observe(e) }

func (t *transcript) finish(res run.Result, err error) {
	defer func() {
		if t.mirror != nil {
			t.mirror.Nudge()
		}
	}()
	t.log.Flush()
	if t.detach != nil {
		t.detach()
	}
	switch {
	case err != nil:
		// One cleaned, capped line: a provider's body or a wrapped error can be
		// long and carry control characters.
		t.log.System("worker turn ended: " + sanitize.Line(err.Error(), 480))
	default:
		t.log.System(fmt.Sprintf("goal %s after %d passes", res.StopReason, res.Passes))
	}
}
