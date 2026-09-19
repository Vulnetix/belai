// Package explore derives read-only investigation tasks from a mode decision
// and runs them as bounded-parallel subagents whose results re-enter the
// parent conversation as untrusted user turns.
//
// Plan is pure (no I/O) and ordered; the runner lives in internal/agent so it
// can construct agent.Session subagents without an import cycle.
package explore

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/clarify"
	"github.com/vulnetix/signet/internal/modes"
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

// RefKind classifies an extracted reference.
type RefKind int

const (
	RefText RefKind = iota
	RefFile
	RefDir
	RefRepo
	RefOrg
	RefURL
)

// ClassifyRef determines the lexical kind of a reference. It performs no I/O
// so the package stays pure and deterministic.
func ClassifyRef(ref string) RefKind {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return RefText
	}

	// URL: explicit scheme or common URL prefixes.
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") ||
		strings.HasPrefix(ref, "ssh://") || strings.HasPrefix(ref, "git@") ||
		strings.Contains(ref, "://") || strings.HasPrefix(ref, "www.") {
		return RefURL
	}

	// File: has a recognisable extension. Prefer this before repository
	// because paths like docs/arch.md look repo-like but are files.
	if hasFileExtension(ref) {
		return RefFile
	}

	// Repository: owner/repo. A single slash with valid slug characters is
	// ambiguous (docs/arch vs owner/repo), so we treat it as a repo when at
	// least one part is capitalised or contains a hyphen/underscore — those
	// shapes are repo slugs far more often than directory paths.
	if looksLikeRepo(ref) {
		parts := strings.Split(ref, "/")
		if hasUpper(parts[0]) || hasUpper(parts[1]) || strings.ContainsAny(parts[0], "-_") || strings.ContainsAny(parts[1], "-_") {
			return RefRepo
		}
		return RefDir
	}

	// Path-like directory: contains a slash or starts with a path prefix.
	if strings.Contains(ref, "/") || strings.HasPrefix(ref, "./") ||
		strings.HasPrefix(ref, "~") || strings.HasPrefix(ref, "/") {
		return RefDir
	}

	// Organisation: a single capitalised identifier.
	if isCapitalisedIdentifier(ref) {
		return RefOrg
	}

	return RefText
}

func looksLikeRepo(ref string) bool {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !isRepoRune(r) {
				return false
			}
		}
	}
	return true
}

func isRepoRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
}

func hasUpper(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func hasFileExtension(ref string) bool {
	base := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		base = ref[i+1:]
	}
	if base == "" {
		return false
	}
	// An extension is a dot not at the start or end with at least one
	// alphanumeric character on each side.
	for i := 1; i < len(base)-1; i++ {
		if base[i] == '.' {
			return true
		}
	}
	return false
}

func isCapitalisedIdentifier(ref string) bool {
	if ref == "" {
		return false
	}
	runes := []rune(ref)
	if runes[0] < 'A' || runes[0] > 'Z' {
		return false
	}
	for _, r := range runes[1:] {
		if !isOrgRune(r) {
			return false
		}
	}
	return true
}

func isOrgRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
}

// localFirstRule is appended to repo/org prompts to steer the model toward
// the local index before calling GH.
const localFirstRule = "\n\nCheck the local index first — call Repos to see which checkouts are on this machine, then RepoFiles/RepoRead for one that is. Only use GH (`gh api repos/.../contents/...`, `gh api repos/.../git/trees/HEAD?recursive=1`) for a repository that is not available locally."

// promptForKind builds an investigation prompt matched to the reference kind.
func promptForKind(ref string, kind RefKind, prompt string) string {
	switch kind {
	case RefFile:
		return fmt.Sprintf("Read the file %q and report what is relevant to: %s", ref, prompt)
	case RefDir:
		return fmt.Sprintf("Explore the directory %q and report what is relevant to: %s", ref, prompt)
	case RefRepo:
		return fmt.Sprintf("Investigate the repository %q for: %s%s", ref, prompt, localFirstRule)
	case RefOrg:
		return fmt.Sprintf("Investigate repositories under %q for: %s%s", ref, prompt, localFirstRule)
	case RefURL:
		return fmt.Sprintf("Fetch and summarize %q for: %s", ref, prompt)
	default:
		return fmt.Sprintf("Investigate %q for: %s", ref, prompt)
	}
}

// Plan derives ordered investigation tasks from a prompt. It is pure and
// deterministic: no I/O, no ordering dependency on execution.
func Plan(prompt string, decision rolemanager.ModeDecision) []Task {
	if !decision.Explore {
		return nil
	}
	refs := extractReferences(prompt)
	// Plan mode with no references gets a repository survey rather than
	// repeating the raw prompt.
	if len(refs) == 0 && decision.Mode == modes.ModePlan {
		return PlanSurvey(prompt)
	}
	if len(refs) == 0 {
		return []Task{{Index: 0, Reference: prompt, Prompt: prompt}}
	}
	tasks := make([]Task, 0, len(refs))
	for i, r := range refs {
		tasks = append(tasks, Task{
			Index:     i,
			Reference: r,
			Prompt:    promptForKind(r, ClassifyRef(r), prompt),
		})
	}
	if len(tasks) > MaxTasks {
		tasks = tasks[:MaxTasks]
	}
	return tasks
}

// PlanClarified derives read-only tasks from the user's clarification answers.
// Skipped groups produce no task; answered groups become one investigation each.
// The result is capped at MaxTasks and is deterministic.
func PlanClarified(prompt string, q clarify.Questionnaire, a clarify.Answers) []Task {
	var tasks []Task
	idx := 0
	for _, ans := range a.Items {
		if ans.Skipped {
			continue
		}
		if ans.GroupIndex < 0 || ans.GroupIndex >= len(q.Groups) {
			continue
		}
		g := q.Groups[ans.GroupIndex]
		if len(ans.Chosen) == 0 && ans.Note == "" {
			continue
		}
		var labels []string
		for _, c := range ans.Chosen {
			if c < 0 || c >= len(g.Options) {
				continue
			}
			labels = append(labels, g.Options[c].Label)
		}
		note := ""
		if ans.Note != "" {
			note = " " + ans.Note
		}
		labelsStr := "(none)"
		if len(labels) > 0 {
			labelsStr = strings.Join(labels, ", ")
		}
		tasks = append(tasks, Task{
			Index:     idx,
			Reference: fmt.Sprintf("clarification %d", idx+1),
			Prompt:    fmt.Sprintf("Investigate: %s The user chose %s.%s Goal: %s", g.Context, labelsStr, note, prompt),
		})
		idx++
		if len(tasks) >= MaxTasks {
			break
		}
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

// PlanSurvey is the plan-mode twin of PlanGoalSurvey: it surveys the
// repository when the prompt gives the model nothing to hold onto.
func PlanSurvey(promptText string) []Task {
	return planSurvey(promptText, nil)
}

// PlanSurveyWithEntrypoints is PlanSurvey enriched with the concrete
// entrypoints from the repository map, so the "entry points" survey task names
// real files instead of asking the model to rediscover them.
func PlanSurveyWithEntrypoints(promptText string, entrypoints []string) []Task {
	return planSurvey(promptText, entrypoints)
}

func planSurvey(promptText string, entrypoints []string) []Task {
	entryPrompt := "Identify the entry points and the modules most relevant to the goal. Goal: " + promptText
	if len(entrypoints) > 0 {
		entryPrompt = fmt.Sprintf("Investigate these entry points: %s. Report how they are wired and which is most relevant to: %s", strings.Join(entrypoints, ", "), promptText)
	}
	surveys := []struct {
		reference string
		prompt    string
	}{
		{"repository structure", "Survey the repository structure: list the top-level directories, the main packages, and how they relate. Also list any locally available related repositories. Goal: " + promptText},
		{"entry points", entryPrompt},
		{"tests and docs", "Find the existing tests and documentation that bear on the goal, and report their locations. Goal: " + promptText},
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
