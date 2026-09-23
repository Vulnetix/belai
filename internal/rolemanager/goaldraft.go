package rolemanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/sanitize"
)

// goalDraftSystemPrompt instructs the classifier to draft a completion
// contract for a goal-mode prompt. The reply is prose, not a sentinel: this
// role returns multi-section text, so it is deliberately absent from
// labels.go's sentinel maps and has no Label() entry.
const goalDraftSystemPrompt = `You are a goal-contract writer for an LLM coding harness. The user's prompt will be carried verbatim as the Objective line. Produce the five remaining sections of a completion contract that an autonomous goal loop can evaluate itself against.

Reply with ONLY the five sections below, using exactly these headings, in this order:

## Verification surface
Concrete, checkable evidence that proves completion: commands to run, files to inspect, and their expected output or state.

## Constraints
Invariants that must keep holding while the work is done. Name security, quality, and compatibility boundaries explicitly.

## Boundaries
What is in scope and what is deliberately out of scope: allowed tools and directories, and changes that must not be made.

## Iteration policy
How the loop should choose the next action between passes, what to re-check before continuing, and how to tell progress from stalling.

## Blocked stop condition
What must be true to stop early: the evidence already gathered, the paths already attempted, the blocker, and the exact next input needed from the user.

Rules:
- Keep every heading above exactly as written; the prose between them is yours.
- Do not repeat the objective line; it is already present.
- Do not output any other headings, Markdown fences, or commentary outside the sections.
- The contract is read by the model doing the work, so be concrete and actionable, not generic.
- Verification commands: when a harness-detected command list is given, use only those commands (plus read-only git). Never invent a build tool or target the list does not name — no make, npm, cargo or similar unless listed.
- Name files only when the user's goal names them or they are certain; never guess paths.
- Keep the whole reply under 600 words; the loop starts editing as soon as this contract exists, so brevity is speed.`

// GoalDraftInput is the material the goal-contract classifier is shown.
type GoalDraftInput struct {
	// Prompt is the user's sanitized goal prompt, carried verbatim as the
	// contract's Objective line.
	Prompt string
	// VerificationSurface is the harness-known default verification surface,
	// e.g. the test commands the repo map detected.
	VerificationSurface []string
}

// BuildGoalDraftPayload constructs the goal-contract request. Tools, Skills,
// and Agent are always empty: the classifier turn must never expose tools,
// skills, or an agent block, exactly like every other classifier payload.
func BuildGoalDraftPayload(in GoalDraftInput) ClassifierPayload {
	return ClassifierPayload{
		System:    goalDraftSystemPrompt,
		User:      goalDraftUser(in),
		MaxTokens: ClassifierStructuredMaxTokens,
		UseCase:   UseCaseGoalContract,
	}
}

func goalDraftUser(in GoalDraftInput) string {
	var b strings.Builder
	b.WriteString("User goal (carried verbatim as Objective, do not repeat it):\n")
	b.WriteString(sanitize.Sanitize(in.Prompt))
	if len(in.VerificationSurface) > 0 {
		b.WriteString("\n\nDefault verification surface (harness-detected commands):\n")
		for _, cmd := range in.VerificationSurface {
			b.WriteString("- " + sanitize.Sanitize(cmd) + "\n")
		}
	}
	return b.String()
}

// ErrGoalDraftUnusable reports that the goal-contract classifier could not
// produce a usable draft. The caller must fall back to the raw prompt so a
// weak drafting model never costs the turn.
var ErrGoalDraftUnusable = errors.New("goal contract draft was not usable")

// DraftGoalContract asks the classifier to draft the sections of a goal
// completion contract and returns the full contract: the user's prompt as the
// verbatim Objective line, then the drafted sections. The drafted text is
// sanitized before it is returned, so delimiter markup cannot forge a harness
// block when the caller seals the contract into the system prompt.
//
// A nil classifier, transport failure, or an empty draft is returned as
// ErrGoalDraftUnusable so the caller can fail open to the raw prompt.
func DraftGoalContract(ctx context.Context, c Classifier, in GoalDraftInput) (string, error) {
	if c == nil {
		return "", ErrGoalDraftUnusable
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", ErrGoalDraftUnusable
	}
	raw, err := c.Classify(ctx, BuildGoalDraftPayload(in))
	if err != nil {
		return "", fmt.Errorf("goal contract draft: %w", err)
	}
	draft := sanitize.Sanitize(raw)
	draft = dropInventedCommands(draft, in.VerificationSurface)
	if strings.TrimSpace(draft) == "" {
		record(EventGoalDraft, "empty", "", "", 0)
		return "", ErrGoalDraftUnusable
	}
	contract := "Objective:\n" + in.Prompt + "\n\n" + draft
	if !strings.Contains(contract, in.Prompt) {
		// Unreachable for a non-empty prompt; kept as the fail-closed guard
		// the caller's fallback contract promises.
		record(EventGoalDraft, "missing_objective", "", "", 0)
		return "", ErrGoalDraftUnusable
	}
	record(EventGoalDraft, "usable", "", fmt.Sprintf("chars=%d", len(contract)), 0)
	return contract, nil
}

// knownRunners are build and test tool names a drafting model tends to invent
// for a repository that does not use them. Sessions showed `make test` written
// into contracts for a repository with no Makefile, and the goal loop then
// spent passes failing a command that could never succeed.
var knownRunners = map[string]bool{
	"make": true, "npm": true, "npx": true, "yarn": true, "pnpm": true, "bun": true,
	"cargo": true, "pytest": true, "tox": true, "poetry": true, "pip": true,
	"go": true, "just": true, "bazel": true, "gradle": true, "gradlew": true,
	"mvn": true, "dotnet": true, "rake": true, "bundle": true, "mix": true,
}

// dropInventedCommands removes contract lines that name a backticked command
// whose runner is not among the harness-detected commands. It is a
// deterministic post-check on the draft: with no detected commands there is
// nothing to check against and the draft is returned unchanged. File paths and
// git commands in backticks are never runners, so they are untouched.
func dropInventedCommands(draft string, detected []string) string {
	if len(detected) == 0 {
		return draft
	}
	allowed := map[string]bool{"git": true}
	for _, c := range detected {
		if f := strings.Fields(c); len(f) > 0 {
			allowed[f[0]] = true
		}
	}
	lines := strings.Split(draft, "\n")
	out := lines[:0]
	for _, line := range lines {
		if !namesInventedRunner(line, allowed) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// namesInventedRunner reports whether a line holds a backticked command whose
// first word is a known runner that allowed does not contain.
func namesInventedRunner(line string, allowed map[string]bool) bool {
	parts := strings.Split(line, "`")
	for i := 1; i < len(parts); i += 2 {
		f := strings.Fields(parts[i])
		if len(f) == 0 {
			continue
		}
		if knownRunners[f[0]] && !allowed[f[0]] {
			return true
		}
	}
	return false
}
