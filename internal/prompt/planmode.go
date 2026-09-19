// Package prompt builds the sealed system-prompt blocks the agent turns carry.
package prompt

// PlanContract is the full planning contract injected on pass 1 and every
// fifth pass thereafter. It is the three-phase Codex contract: ground before
// asking, decide intent, then specify implementation — and it forbids calling
// the plan final until the implementer would make no decisions.
const PlanContract = `Plan in three phases, in order, before you finish.

Phase 1 — Ground in the environment. Explore the repository and verify facts
before asking the user anything. Never ask a question the repository can
answer: read the files, run the read-only tools, and establish the real layout,
build, and test commands first.

Phase 2 — Intent. State the goal, the success criteria, the scope and
non-goals, the constraints, and the tradeoffs you are choosing. If any of
these are genuinely ambiguous after Phase 1, ask the one question that matters
most; otherwise decide them.

Phase 3 — Implementation. Specify interfaces, data flow, edge cases, the test
plan, and the acceptance check for each step. Group work by subsystem, not as a
file-by-file list.

Finalize only when the plan is decision complete: an implementer following it
would make no decisions of their own. The plan must be compact and structured
as ## Summary, ## Key Changes, ## Test Plan, and ## Assumptions (plus ## Risks
when any exist), with each Key Change a numbered step carrying the files it
touches and how to verify it.`

// PlanReminder is the one-line reminder injected on passes between full
// injections, so the contract's discipline survives a long planning turn
// without re-spending the tokens of the full contract every pass.
const PlanReminder = "Continue the plan using the three-phase contract: ground, then intent, then implementation. Finish with ## Summary, ## Key Changes, ## Test Plan, and ## Assumptions."

// PlanDirective returns the planning directive for a 1-based pass number:
// full on pass 1, a one-line reminder on each later pass, and a full
// re-injection every five passes.
func PlanDirective(pass int) string {
	if (pass-1)%5 == 0 {
		return PlanContract
	}
	return PlanReminder
}
