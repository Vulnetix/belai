package agent

import (
	"fmt"
	"time"

	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/run"
)

// recordPlan writes the model's reply to a plan file on every plan-mode exit
// path. It is intentionally a no-op when allowPassLoop is false: only the
// top-level plan pass loop produces a plan review artifact.
//
// Write failures are emitted as warnings and never abort the turn: the user
// still sees the reply in the transcript and can act on it.
func (s *Session) recordPlan(res run.Result, prompt string, emit func(Event)) run.Result {
	if !s.allowPassLoop || res.PlanSentinel == "" {
		return res
	}
	if res.Reply == "" {
		// Nothing to record.
		return res
	}

	now := time.Now()
	rev := 1
	base := plans.RecordName(prompt, now, 1)
	if s.planRevision > 0 {
		rev = s.planRevision
	} else {
		rev = plans.NextRevision(s.workdir, base)
	}
	plan, path, err := plans.Record(s.workdir, prompt, res.Reply, now, rev)
	if err != nil {
		emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("plan file not recorded: %v", err)})
		return res
	}
	res.PlanPath = path
	res.PlanName = plan.Name
	emit(Event{Kind: EventPlanFileKind, PlanPath: path, PlanName: plan.Name})
	return res
}
