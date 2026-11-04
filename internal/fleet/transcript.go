package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/turnlog"
)

// transcript writes a worker item's session, keyed by the trusted
// repository — not the worktree — so it lists with the project's other
// sessions, resumes, and reaches ReadSession and SearchSessions.
type transcript struct {
	log    *turnlog.Log
	mirror *sessionsync.Syncer
	// id is the session the mirror ends with this turn's outcome, and claim
	// the item claim whose recorded outcome (KanbanOutcome) it reads.
	id    string
	claim *tools.WorkerClaim
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
	return &transcript{log: log, mirror: w.mirror, id: t.SessionID, claim: t.Claim, detach: log.AttachRoleManager()}
}

func (t *transcript) user(text string) { t.log.User(text, nil) }

func (t *transcript) observe(e agent.Event) { t.log.Observe(e) }

// finish closes the turn's transcript and tells the mirror how the turn
// ended, before the next turn's session replaces this one. ctx is the item's
// context, whose cause says whether a budget or a lost lease stopped it.
func (t *transcript) finish(ctx context.Context, res run.Result, err error) {
	defer func() {
		if t.mirror != nil {
			t.mirror.SetEndReasonFor(t.id, turnEnd(res, err, context.Cause(ctx), recordedOutcome(t.claim, res, err, context.Cause(ctx))))
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

// turnEnd is the session end a worker turn reports: what the worker recorded
// for its item, else what stopped the item's context, else the turn's error,
// else how the goal loop stopped. The reasons are sessionsync's, the set the
// website accepts. Error carries harness text only (a fixed phrase, a cause,
// or the cleaned run error), never the model's words.
func turnEnd(res run.Result, err, cause error, rec tools.Outcome) sessionsync.EndInfo {
	switch rec {
	case tools.OutcomeSuccess:
		return sessionsync.EndInfo{Reason: sessionsync.EndGoalComplete}
	case tools.OutcomeFailure:
		return sessionsync.EndInfo{Reason: sessionsync.EndError, Error: "the worker recorded failure"}
	case tools.OutcomeBlocked:
		return sessionsync.EndInfo{Reason: sessionsync.EndGoalPaused, Error: "the worker recorded that it cannot continue"}
	}
	switch {
	case errors.Is(cause, errWallBudget):
		return sessionsync.EndInfo{Reason: sessionsync.EndTimeout, Error: cause.Error()}
	case errors.Is(cause, errTokenBudget), errors.Is(cause, errCostBudget):
		return sessionsync.EndInfo{Reason: sessionsync.EndBudgetExceeded, Error: cause.Error()}
	case cause != nil:
		return sessionsync.EndInfo{Reason: sessionsync.EndInterrupted, Error: sanitize.Line(cause.Error(), 480)}
	case err != nil:
		return sessionsync.EndInfo{Reason: EndReason(err), Error: sanitize.Line(err.Error(), 480)}
	case len(res.AsksWithheld) > 0:
		return sessionsync.EndInfo{Reason: sessionsync.EndGoalPaused, Error: "needs permission: " + sanitize.Line(strings.Join(res.AsksWithheld, ", "), 400)}
	}
	switch res.StopReason {
	case "":
		return sessionsync.EndInfo{Reason: sessionsync.EndCompleted}
	case run.StopComplete, run.StopDecided, run.StopCoordinated:
		return sessionsync.EndInfo{Reason: sessionsync.EndGoalComplete}
	case run.StopMaxPasses:
		return sessionsync.EndInfo{Reason: sessionsync.EndMaxTurns}
	case run.StopCancelled:
		return sessionsync.EndInfo{Reason: sessionsync.EndInterrupted}
	case run.StopError:
		return sessionsync.EndInfo{Reason: sessionsync.EndError}
	}
	// Stalled, withheld, evaluator, incomplete: the goal stopped short.
	return sessionsync.EndInfo{Reason: sessionsync.EndGoalPaused, Error: "goal " + string(res.StopReason)}
}
