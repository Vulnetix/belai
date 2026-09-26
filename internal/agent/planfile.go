package agent

import (
	"fmt"
	"time"

	"github.com/vulnetix/belai/internal/plans"
	"github.com/vulnetix/belai/internal/run"
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
	// The deliberately authored plan (the ExitPlanMode plan argument) is the
	// authoritative artifact. When the turn exited through the evaluator with
	// no ExitPlanMode call, fall back to the latest reply so no path loses its
	// artifact.
	content := res.PlanText
	if content == "" {
		content = res.Reply
	}
	if content == "" {
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
	plan, path, err := plans.Record(s.workdir, prompt, content, now, rev)
	if err != nil {
		emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("plan file not recorded: %v", err)})
		return res
	}
	res.PlanPath = path
	res.PlanName = plan.Name
	emit(Event{Kind: EventPlanFileKind, PlanPath: path, PlanName: plan.Name})
	return res
}
