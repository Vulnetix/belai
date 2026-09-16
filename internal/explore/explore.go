// Package explore derives read-only investigation tasks from a mode decision
// and runs them as bounded-parallel subagents whose results re-enter the
// parent conversation as untrusted user turns.
//
// Plan is pure (no I/O) and ordered; the runner lives in internal/agent so it
// can construct agent.Session subagents without an import cycle.
package explore

import (
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// MaxTasks is the hard cap on fan-out. Unbounded fan-out against a
// rate-limited provider produces 429s, which is worse than sequential.
const MaxTasks = 5

// Task is one reference to investigate.
type Task struct {
	Index     int
	Reference string // the @file, URL, or prompt fragment to investigate
	Prompt    string // the subagent's investigation prompt
}

// Plan derives ordered investigation tasks from a prompt. It is pure and
// deterministic: no I/O, no ordering dependency on execution.
func Plan(prompt string, decision rolemanager.ModeDecision) []Task {
	if !decision.Explore {
		return nil
	}
	refs := extractReferences(prompt)
	if len(refs) == 0 {
		return []Task{{Index: 0, Reference: prompt, Prompt: prompt}}
	}
	tasks := make([]Task, 0, len(refs))
	for i, r := range refs {
		tasks = append(tasks, Task{
			Index:     i,
			Reference: r,
			Prompt:    "Investigate " + r + " for: " + prompt,
		})
	}
	if len(tasks) > MaxTasks {
		tasks = tasks[:MaxTasks]
	}
	return tasks
}

// PlanGoalSurvey derives read-only codebase-survey tasks for a goal prompt
// that has no @references. A forced explore must not simply re-ask the
// original question — it has to survey the repository so the goal pass has
// real evidence to plan from. It is pure and deterministic.
func PlanGoalSurvey(goalText string) []Task {
	surveys := []struct {
		reference string
		prompt    string
	}{
		{"repository structure", "Survey the repository structure: list the top-level directories, the main packages, and how they relate. Goal: " + goalText},
		{"entry points", "Identify the entry points and the modules most relevant to the goal. Goal: " + goalText},
		{"tests and docs", "Find the existing tests and documentation that bear on the goal, and report their locations. Goal: " + goalText},
	}
	tasks := make([]Task, 0, len(surveys))
	for i, s := range surveys {
		tasks = append(tasks, Task{Index: i, Reference: s.reference, Prompt: s.prompt})
	}
	return tasks
}

// extractReferences returns the @-prefixed tokens in prompt, excluding the
// @agent:NAME directive (that engages a named agent, not an explore task),
// deduplicated and in first-seen order.
func extractReferences(prompt string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Fields(prompt) {
		if !strings.HasPrefix(tok, "@") || len(tok) == 1 {
			continue
		}
		ref := strings.TrimPrefix(tok, "@")
		if before, _, ok := strings.Cut(ref, ":"); ok && before == "agent" {
			continue
		}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	sort.Strings(out) // deterministic join order regardless of prompt order
	return out
}
