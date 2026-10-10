package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/plans"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
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
	// Only a plan is recorded as a plan. The fallback reply is the model's
	// last text, which on a failed turn is a sentence about the research
	// ("Updated plan status now."), not a plan; writing that to disk put a
	// non-plan in front of the reviewer (session d9292a3d). The reply still
	// reaches the transcript; the warning says why there is no file.
	if res.PlanText == "" && !hasPlan(content) {
		emit(Event{Kind: EventWarningKind, Warning: "no plan file recorded: the turn ended without a plan (no numbered steps); send the request again or refine with what is missing"})
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

// maxLintReplyRunes bounds one line of the rejection the model reads.
const maxLintReplyRunes = 300

// lintPlanOnce checks the plan an ExitPlanMode call carries with plans.Lint.
// On the first flawed plan of a turn it returns the tool result that sends the
// plan back, naming each flaw; every later call of the turn, and any plan
// without a flaw, returns "" and is accepted. A plan is therefore never
// rejected twice, so the check can cost one round and cannot loop.
//
// The reply is fixed wording plus step numbers and paths from the model's own
// plan, sanitised and flattened to one line each. It is an ordinary tool
// result, never a sealed harness block.
func (s *Session) lintPlanOnce(args map[string]any, final bool) string {
	// On the loop's finishing pass there is no pass left to correct a plan in (a
	// rejection there cost a whole extra pass), so it is accepted as written. On
	// any other pass's last round a rejection is fine: the pass is given one more
	// round for the correction.
	if s.planLinted || final {
		return ""
	}
	plan, _ := args["plan"].(string)
	issues := plans.LintAt(sanitize.Sanitize(plan), s.lintRoots()...)
	// A stray run of foreign-script letters is rare and always a slip, unless
	// the request itself was written in that script.
	if stray := plans.StrayScript(plan); stray != "" && plans.StrayScript(s.turnPrompt) == "" {
		issues = append([]string{fmt.Sprintf("the plan contains stray non-English text (%q); rewrite that sentence in English", stray)}, issues...)
		if len(issues) > 6 {
			issues = issues[:6]
		}
	}
	if len(issues) == 0 {
		// A plan of reviewMinSteps or more steps that has no mechanical flaw is
		// still sent back once with the review checklist: the flaws left in large
		// plans are the ones a program cannot see (order, scope, a Verify that does
		// not test the step).
		if d, err := plans.ParseDoc(sanitize.Sanitize(plan)); err == nil && len(d.Steps) >= reviewMinSteps {
			s.planLinted = true
			s.lintRejected = sanitize.Sanitize(plan)
			return planReviewChecklist
		}
		return ""
	}
	s.planLinted = true
	s.lintRejected = sanitize.Sanitize(plan)
	var b strings.Builder
	b.WriteString("plan not accepted yet; fix these and call ExitPlanMode again with the whole corrected plan:")
	for _, is := range issues {
		line := strings.Join(strings.Fields(sanitize.Sanitize(is)), " ")
		if r := []rune(line); len(r) > maxLintReplyRunes {
			line = string(r[:maxLintReplyRunes]) + "…"
		}
		b.WriteString("\n- " + line)
	}
	return b.String()
}

// lintRoots are the directories the plan lint checks the plan's paths and
// commands against: the session's working directory and every added workspace
// root. Empty (no filesystem rules) when the session has neither.
func (s *Session) lintRoots() []string {
	var roots []string
	if s.registry != nil {
		if c := s.registry.Cwd(); c != nil {
			roots = append(roots, c.Roots()...)
		}
	}
	if len(roots) == 0 && s.workdir != "" {
		roots = []string{s.workdir}
	}
	return roots
}

// reviewMinSteps is the plan size from which the review checklist is sent.
const reviewMinSteps = 5

// planReviewChecklist is the fixed review a large plan gets once. It restates
// the scoring rubric's points as instructions.
const planReviewChecklist = `plan check before it is accepted. Re-read the plan against these points, fix any that fail, and call ExitPlanMode again with the whole plan (the same plan if every point already holds):
- Every file a step edits is listed in that step's Files, and each is a real path or marked (new).
- Each Verify is one command that runs from the right directory, exercises that step's own change, and runs only tests that exist by that step.
- The project builds and its existing tests pass after every step: a signature change and all its callers are in one step, and an existing test is updated in the step that breaks it.
- Every test in the Test Plan is written by a step, and nothing the request asks for is missing.
- Nothing speculative: no step the request does not need, no abandoned approach, no placeholder or hedge.`
