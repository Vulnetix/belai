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
// rate-limited provider produces 429s, which is worse than sequential. The
// cap is now 12 so the default 15-agent pool can run a broad exploration
// wave while leaving headroom for background agents.
const MaxTasks = 12

// Task is one reference to investigate.
type Task struct {
	Index     int
	Reference string // the @file, URL, or prompt fragment to investigate
	Prompt    string // the subagent's investigation prompt
	// Kind is the reference's lexical kind; survey and clarify tasks are
	// RefText. The runner attaches the local-repository index only to
	// RefRepo/RefOrg tasks.
	Kind RefKind
	// Budget is the task's tool-round budget. The runner takes the smaller
	// of it and resilience.max_explore_iterations; zero means the setting.
	Budget int
	// Evidence is an already-classified report the subagent investigates,
	// handed to it as a file attachment labelled EvidenceLabel rather than
	// spliced into the prompt. Only review tasks carry one.
	Evidence      string
	EvidenceLabel string
	// ReportBytes bounds the subagent's report; zero means the runner's
	// default. A review report lists every finding, so it gets more room
	// than a ten-bullet survey answer.
	ReportBytes int
}

// Tool-round budgets. Each task is one narrow question, so it gets only the
// rounds that question needs: a subagent that must answer in a few rounds
// searches instead of browsing. Many small subagents in parallel finish
// sooner than a few broad ones, and a spent budget still ends on a report.
const (
	budgetFile   = 2 // read one file (usually already attached; see DropAttached)
	budgetURL    = 2 // one fetch, one summary
	budgetLocate = 4 // grep/glob, then confirm the hits
	budgetFocus  = 3 // one narrow lookup: tests, docs, a call path, a clarified choice
	budgetRepo   = 5 // a repository is bigger than a file
	budgetReview = 6 // locate and weigh every finding of one scanner report
)

// reportContract ends every task prompt. Findings re-enter the parent as
// context and pass the classifier, so a terse, located report is both faster
// and more useful to the planner than a narrative survey.
const reportContract = "\n\nStop as soon as you can answer; do not survey beyond the question. " +
	"The repository map and status are already in your context — do not re-list the layout or git state. " +
	"Reply with at most 10 bullets, each `path:line — fact` (or `path — fact` when there is no line), most relevant first, with no preamble and no narrative. " +
	"If nothing relevant exists, say so in one line."

// SurveyReference is the reference label of the first plan-survey task, so
// the runner can recognise a survey and enrich it with the map's entrypoints.
const SurveyReference = "locate"

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
	var p string
	switch kind {
	case RefFile:
		p = fmt.Sprintf("Read the file %q and name the parts of it that matter for: %s", ref, prompt)
	case RefDir:
		p = fmt.Sprintf("In the directory %q, find the files that matter for the goal (Glob/Grep first, read only the best hits). Goal: %s", ref, prompt)
	case RefRepo:
		p = fmt.Sprintf("In the repository %q, find the code that matters for: %s%s", ref, prompt, localFirstRule)
	case RefOrg:
		p = fmt.Sprintf("Find which repositories under %q matter for: %s%s", ref, prompt, localFirstRule)
	case RefURL:
		p = fmt.Sprintf("Fetch %q and extract only what matters for: %s", ref, prompt)
	default:
		p = fmt.Sprintf("Find where %q lives in the code and what matters about it for: %s", ref, prompt)
	}
	return p + reportContract
}

// budgetForKind is the tool-round budget of a reference task.
func budgetForKind(kind RefKind) int {
	switch kind {
	case RefFile:
		return budgetFile
	case RefURL:
		return budgetURL
	case RefRepo, RefOrg:
		return budgetRepo
	case RefDir:
		return budgetFocus
	}
	return budgetLocate
}

// DropAttached removes the file tasks whose file already rides on the turn
// as an attachment: the planner has the whole file, so a subagent reading it
// again only to summarise it is a wasted round trip. attached holds
// root-relative, slash-separated paths. The remaining tasks are re-indexed.
func DropAttached(tasks []Task, attached map[string]bool) []Task {
	if len(attached) == 0 {
		return tasks
	}
	out := tasks[:0:0]
	for _, t := range tasks {
		if t.Kind == RefFile && attached[strings.TrimPrefix(t.Reference, "/")] {
			continue
		}
		t.Index = len(out)
		out = append(out, t)
	}
	return out
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
		return []Task{{Index: 0, Reference: prompt, Prompt: "Find the code that matters for this request and where it lives. Request: " + prompt + reportContract, Budget: budgetLocate}}
	}
	tasks := make([]Task, 0, len(refs))
	for i, r := range refs {
		kind := ClassifyRef(r)
		tasks = append(tasks, Task{
			Index:     i,
			Reference: r,
			Prompt:    promptForKind(r, kind, prompt),
			Kind:      kind,
			Budget:    budgetForKind(kind),
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
			Prompt:    fmt.Sprintf("Find what the user's choice means in the code. Question: %s The user chose %s.%s Goal: %s%s", g.Context, labelsStr, note, prompt, reportContract),
			Budget:    budgetFocus,
		})
		idx++
		if len(tasks) >= MaxTasks {
			break
		}
	}
	return tasks
}

// PlanGoalSurvey derives read-only survey tasks for a goal prompt that has no
// @references. A forced explore must not simply re-ask the original question,
// and in goal mode it must not return a structural essay either: the goal
// pass is about to edit, so the survey's job is to name the edit targets and
// the check that will prove them right. It is pure and deterministic.
func PlanGoalSurvey(goalText string) []Task {
	return survey([]surveyTask{
		{"edit targets", "Name the exact files and line ranges that must change for this goal (Grep/Glob for its key terms first). Goal: " + goalText, budgetLocate},
		{"verification", "Name the existing tests and the command that runs them for the code this goal changes. Goal: " + goalText, budgetFocus},
	})
}

// ReviewReportBytes bounds one scanner subagent's report. Its contract is one
// line per finding rather than ten bullets, so it is larger than a survey's.
const ReviewReportBytes = 16 * 1024

// ReviewReport is one scanner's bounded, already-classified report from a
// /vulnetix review.
type ReviewReport struct {
	Scanner string
	Label   string
	Body    string
}

// ReviewFinding is one scanner subagent's finished report, run ahead of the
// triage turn (as a background agent while the other scanners were still
// running) and already sanitized and, under guardrails, classified. The
// triage turn seals it as an exploration turn instead of running that
// scanner's subagent again.
type ReviewFinding struct {
	Scanner string
	Label   string
	Body    string
}

// reviewContract ends every review task prompt. The subagent is read-only: it
// grounds each finding in the repository and names the remediation paths, and
// the parent session — which sees every scanner's report together — decides,
// fixes, asks the user, or records the finding as inconclusive.
const reviewContract = "\n\nYou are read-only: do not attempt the fix. " +
	"The repository map and status are already in your context — do not re-list the layout or git state. " +
	"Reply with one line per finding, most severe first, with no preamble and no narrative, in the form " +
	"`path:line | rule/id | verdict | remediation`, where verdict is real, false-positive or unclear, and remediation is one of " +
	"`fix: <the single change>`, `options: <A> | <B> [| …]` when more than one reasonable fix exists and the choice needs the user, " +
	"or `none: <why neither a fix nor further exploration can settle it>`. " +
	"Findings that share a root cause may share one line. If the report has no actionable finding, say so in one line."

// PlanReview derives one read-only task per scanner report of a /vulnetix
// review. Each subagent receives its scanner's report as evidence and reports
// back located findings with their remediation paths, which the parent session
// then acts on. It is pure and deterministic, and capped at MaxTasks.
func PlanReview(prompt string, reports []ReviewReport) []Task {
	var tasks []Task
	for _, r := range reports {
		if strings.TrimSpace(r.Body) == "" {
			continue
		}
		name := r.Scanner
		if name == "" {
			name = r.Label
		}
		tasks = append(tasks, Task{
			Index:         len(tasks),
			Reference:     "vulnetix " + name,
			Prompt:        fmt.Sprintf("The attached %q is one scanner's findings from a Vulnetix review of this repository. For each finding, read the flagged code and its callers (Grep/Read the reported location first), decide whether it is real, and name how it would be remediated in this codebase. Review goal: %s%s", r.Label, prompt, reviewContract),
			Budget:        budgetReview,
			Evidence:      r.Body,
			EvidenceLabel: r.Label,
			ReportBytes:   ReviewReportBytes,
		})
		if len(tasks) >= MaxTasks {
			break
		}
	}
	return tasks
}

// surveyTask is one entry of a fixed survey.
type surveyTask struct {
	reference, prompt string
	budget            int
}

// survey turns a fixed survey into tasks, each ending in the report contract.
func survey(st []surveyTask) []Task {
	tasks := make([]Task, 0, len(st))
	for i, s := range st {
		tasks = append(tasks, Task{Index: i, Reference: s.reference, Prompt: s.prompt + reportContract, Budget: s.budget})
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

// planSurvey splits the survey into narrow questions that run in parallel.
// There is no "repository structure" task: every subagent already has the
// repository map (layout, languages, commands, entrypoints) in its context,
// so re-surveying it was the slowest task and the least useful.
func planSurvey(promptText string, entrypoints []string) []Task {
	st := []surveyTask{
		{SurveyReference, "Find the code this goal touches: Grep/Glob for its key terms and name the files, functions and line ranges. Goal: " + promptText, budgetLocate},
	}
	if len(entrypoints) > 0 {
		st = append(st, surveyTask{"call path", fmt.Sprintf("Which of these entry points reaches the code this goal touches, and through which calls: %s. Goal: %s", strings.Join(entrypoints, ", "), promptText), budgetFocus})
	}
	st = append(st,
		surveyTask{"tests", "Find the tests that cover the code this goal touches and the command that runs them. Goal: " + promptText, budgetFocus},
		surveyTask{"docs and config", "Find the documentation, configuration and flags that describe or control what this goal changes. Goal: " + promptText, budgetFocus},
	)
	return survey(st)
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
