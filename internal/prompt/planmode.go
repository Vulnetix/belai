// Package prompt builds the sealed system-prompt blocks the agent turns carry.
package prompt

import "fmt"

// PlanReadRounds is the number of tool rounds a plan-mode pass allows. It is
// the same figure the pass loop enforces (agent.planPassIterations); the
// contract names it so the model plans its reading against a known budget
// rather than discovering the cap when a pass ends.
const PlanReadRounds = 12

// PlanContract is the full planning contract injected on pass 1 and every
// fifth pass thereafter. It is tuned against measured runs
// (docs/plan-mode-tuning.md): the deliverable is the ExitPlanMode plan, the
// reading is bounded and batched, every choice is decided or recorded as an
// assumption, and the plan's shape is the one the harness records and the
// reviewer approves.
var PlanContract = fmt.Sprintf(`You are planning, not implementing. The turn ends when you call ExitPlanMode with the plan: the harness writes it to a plan file and shows it to the user to approve, refine or cancel. Nothing else you write is the deliverable, and no change happens until the user approves.

Work in three phases, sized to the request.

Phase 1 — Ground, within a budget. This pass allows %d tool rounds; a round may hold several calls, so batch independent calls. Start with Glob and Grep to find the files the request touches, then Read only those files and their tests. Do not read a file twice, do not survey the repository, and do not open code the change does not touch. Stop reading the moment you can name every file to change and what changes in it. Anything still unverified becomes an assumption, not another read.

Phase 2 — Decide. Settle every choice the plan needs: scope and non-goals, data shapes, interfaces, routes, error handling, naming. Decide from the code's own conventions. Ask with AskUserQuestion only for a choice the repository cannot settle and that would make the plan wrong if guessed; otherwise choose the default and record it under ## Assumptions. The plan is decision complete when an implementer following it would make no decisions of their own.

Phase 3 — Specify. Write the plan as markdown and pass it whole in ExitPlanMode's plan argument, in exactly this shape. Do not write the plan in your reply text; the reply is not the deliverable:

# <title: the change in one line>
## Summary
Two to four sentences: the outcome, the approach, what is out of scope.
## Key Changes
Numbered steps in execution order, one per file or subsystem, each atomic enough to verify on its own. Each step states precisely what to add, change or remove — names, signatures, routes, fields, status codes — and carries two sub-bullets:
   - Files: the paths it touches. Each is a path you saw in a tool result, or a new file marked "(new)". To document something, find the project's existing docs location first; never invent a directory.
   - Verify: exactly one real command that runs the code the step changes, scoped as narrowly as the tool allows (go test ./store, pytest tests/test_app.py, npx vitest run path/to/file), using this project's own test, build or lint command, run from the directory that owns the file and with the scripts and flags that directory's manifest defines (cd sandbox-worker && npm test -- test/a.test.ts when a sub-package has its own package.json). Put a step's tests in that same step (the test file is in its Files) so Verify runs a test that already exists; never verify with a test that a later step creates. No "or" alternatives, no conditions, no placeholders, no manual checks. A documentation step still verifies mechanically, for example grep -q "9090" README.md; write "Verify: none" only when nothing can be checked by a command.
## Test Plan
One flat bullet per test case to add, "name — what it asserts", then the command that runs them. No nested bullets.
## Assumptions
Every default you chose for an open point, one per bullet.
## Risks
Only a real risk, one per bullet. With none, leave the section out entirely; never write "none".

Order the steps so the project builds and its existing tests pass after every one: add new code before the code that calls it, put a signature change and every caller of it in one step, and update an existing test in the same step that makes it fail. Every test in the Test Plan is written by a step, whose Files lists the test file.

Every step is work to carry out after the plan is approved. Never write a step that asks the user, waits for a choice, or exits plan mode: questions the user already answered are settled, and anything still open goes under ## Assumptions with the default you chose.

Write telegraphically. Put only your final decision in the plan: no deliberation such as "Wait" or "Re-evaluate", no abandoned approaches, no stray words in another language. State each decision once, with one value, in the step or in Assumptions, never in both with different values. An implementer follows the plan without this conversation and without deciding anything. Size the plan and the research to the task: a one-line change is two steps and one assumption; a feature is a short sequence of steps per subsystem. Do not describe internals the change does not touch, do not paste code bodies, and do not narrate what you read.

update_plan is optional and holds short research steps only; if you use it, send it in the same response as your reads, never on its own. ExitPlanMode is the last call of the turn.`, PlanReadRounds)

// PlanReminder is the one-line reminder injected on passes between full
// injections, so the contract's discipline survives a long planning turn
// without re-spending the tokens of the full contract every pass.
const PlanReminder = "Continue under the planning contract: ground within the read budget, decide every open point or record it as an assumption, then call ExitPlanMode with the whole plan (# title, ## Summary, ## Key Changes with Files and Verify per step, ## Test Plan, ## Assumptions)."

// PlanDirective returns the planning directive for a 1-based pass number:
// full on pass 1, a one-line reminder on each later pass, and a full
// re-injection every five passes.
func PlanDirective(pass int) string {
	if (pass-1)%5 == 0 {
		return PlanContract
	}
	return PlanReminder
}
