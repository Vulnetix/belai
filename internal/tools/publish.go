package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PublishBranchName is the tool's name. No harness the models were trained
// on has an equivalent — they push with git and open pull requests with gh —
// so this diverges deliberately: a fleet worker must never push anything but
// its own branch, and a raw `git push` can push any ref anywhere. The tool
// pushes exactly the item's branch to origin and opens (or finds) a draft
// pull request for it; `git push`, `gh pr create` and `glab mr create` are
// denied in Bash on a worker.
const PublishBranchName = "PublishBranch"

// Publisher pushes the session's branch and opens or finds its draft pull
// request, returning the link. The fleet implements it over the worker's
// worktree (fleet.Workspace).
type Publisher interface {
	PublishBranch(ctx context.Context, title, body string) (string, error)
}

// PublishBranch is the fleet worker's only way to push.
type PublishBranch struct {
	P Publisher
	// Branch is shown in the description so the model knows what it pushes.
	Branch string
}

// Definition describes the tool.
func (t PublishBranch) Definition() Definition {
	return Definition{
		Name: PublishBranchName,
		Description: fmt.Sprintf("Push this worktree's branch (%s) to origin and open a draft pull request for it, or return the one already open. "+
			"Commit your work first (git add / git commit); only committed work is pushed. It pushes only this branch — "+
			"`git push`, `gh pr create` and `glab mr create` are not available here. Call it again after further commits to push them.", t.Branch),
		Properties: map[string]Property{
			"title": {Type: "string", Description: "The pull request title: what the change does (at most 120 characters)."},
			"body":  {Type: "string", Description: "The pull request description: what changed, why, and how it was verified."},
		},
		Required: []string{"title"},
	}
}

// Kind is the harness-composed confirmation.
func (PublishBranch) Kind() Kind { return KindPublish }

// Subject is the branch, for permission rules such as PublishBranch(belai/*).
func (t PublishBranch) Subject(map[string]any) string { return t.Branch }

// Execute publishes.
func (t PublishBranch) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if t.P == nil {
		return Result{}, errors.New("publishing is not available for this agent")
	}
	title, _ := argString(args, "title")
	body, _ := argString(args, "body")
	title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
	if title == "" {
		return Result{}, errors.New("PublishBranch needs a title")
	}
	if r := []rune(title); len(r) > 120 {
		title = string(r[:120])
	}
	if len(body) > 16<<10 {
		body = body[:16<<10]
	}
	url, err := t.P.PublishBranch(ctx, title, body)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindPublish, Content: fmt.Sprintf("pushed %s; draft pull request: %s", t.Branch, url), Meta: map[string]any{"pr": url}}, nil
}

var _ Tool = PublishBranch{}
