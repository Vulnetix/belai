package agentprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
)

// EphemeralPrefix starts the name of a profile made from a prompt.
const EphemeralPrefix = "adhoc-"

// EphemeralOptions describe a worker that has no stored profile: the prompt that
// stands in for its persona, and the few facts about the work the harness needs.
// Everything else (how the run is explained to the model, how the card is routed,
// the gates and the checks) is the harness's, so a prompt with no profile behind
// it reaches the same outcomes as one that has.
type EphemeralOptions struct {
	// Prompt is the worker's instructions. Required.
	Prompt string
	// Tools is the allowlist. Empty keeps the full surface.
	Tools []string
	// Lists and Labels select what the worker claims; the default list is backlog.
	Lists  []string
	Labels []string
	// To is the list a success goes to. By default review for a worker that
	// changes files, done for one that decides.
	To string
	// Publish is the publish mode of a worker that changes files; the default is
	// agent (the worker publishes its branch itself).
	Publish string
	// ReadOnly runs the checks in a throwaway worktree and changes nothing.
	ReadOnly bool
}

// Ephemeral builds a worker profile from a prompt. It is validated like any
// stored profile, so a prompt that cannot be a worker (an unknown tool, a list
// the board does not have) is refused here.
func Ephemeral(o EphemeralOptions) (AgentProfile, error) {
	prompt := strings.TrimSpace(o.Prompt)
	if prompt == "" {
		return AgentProfile{}, errors.New("a prompt-only worker needs a prompt")
	}
	sum := sha256.Sum256([]byte(prompt))
	p := AgentProfile{
		Name:          EphemeralPrefix + hex.EncodeToString(sum[:4]),
		Description:   "a worker made from a prompt, with no stored profile",
		SystemPrompt:  prompt,
		Mode:          ModeWorker,
		Autonomy:      AutonomyAutonomous,
		MaxIterations: 12,
		Tools:         slices.Clone(o.Tools),
		Ephemeral:     true,
	}
	lists := slices.Clone(o.Lists)
	if len(lists) == 0 {
		lists = []string{"backlog"}
	}
	p.Kanban = &KanbanSpec{Lists: lists, Labels: slices.Clone(o.Labels), MaxAttempts: DefaultMaxAttempts}
	// Whether the worker changes files decides where it works and where its
	// success goes; the tools and read_only say so, as for any profile.
	writes := p.Writes() && !o.ReadOnly
	switch {
	case o.ReadOnly:
		p.Workspace = &WorkspaceSpec{Isolation: IsolationWorktree, ReadOnly: true, Publish: PublishNone}
	case writes:
		publish := o.Publish
		if publish == "" {
			publish = PublishAgent
		}
		p.Workspace = &WorkspaceSpec{Isolation: IsolationWorktree, Publish: publish}
	}
	to := o.To
	if to == "" {
		to = "review"
		if p.Decides() {
			to = "done"
		}
	}
	p.Kanban.OnSuccess = Route{List: to}
	p.Budget = &BudgetSpec{MaxPassesPerItem: 8, MaxWallPerItem: "45m"}
	if err := p.Validate(); err != nil {
		return AgentProfile{}, err
	}
	return p, nil
}
