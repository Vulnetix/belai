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

// CloseDuplicatePRName is the tool's name. Like PublishBranch it has no trained
// equivalent (models close pull requests with gh, which a worker cannot run):
// it closes only one of the item's own open pull requests, as a duplicate of
// another of them.
const CloseDuplicatePRName = "CloseDuplicatePR"

// DuplicateCloser closes one of the item's open pull requests as a duplicate of
// another and returns what it did. The fleet implements it over the worker's
// worktree.
type DuplicateCloser interface {
	CloseDuplicatePR(ctx context.Context, number, keep int, reason string) (string, error)
}

// CloseDuplicatePR is offered when the item has more than one open pull
// request, one per attempt branch.
type CloseDuplicatePR struct {
	C DuplicateCloser
	// Open lists the item's open pull requests for the description.
	Open string
}

// Definition describes the tool.
func (t CloseDuplicatePR) Definition() Definition {
	return Definition{
		Name: CloseDuplicatePRName,
		Description: "Close one of this item's open pull requests as a duplicate of another of them, with a comment naming the one kept. " +
			"Only the item's own pull requests can be named: " + t.Open + ". " +
			"If you close the pull request of this worktree's branch, the item's pull request and branch become the kept one's, and the item returns to its list so the work continues there.",
		Properties: map[string]Property{
			"close":  {Type: "integer", Description: "The number of the pull request to close."},
			"keep":   {Type: "integer", Description: "The number of the pull request it duplicates, which stays open."},
			"reason": {Type: "string", Description: "Why the kept one is the right one, for the closing comment (at most 500 characters)."},
		},
		Required: []string{"close", "keep", "reason"},
	}
}

// Kind is the harness-composed confirmation.
func (CloseDuplicatePR) Kind() Kind { return KindPublish }

// Subject is the pull request closed, for permission rules.
func (CloseDuplicatePR) Subject(args map[string]any) string {
	n, _ := argInt(args, "close")
	return fmt.Sprintf("#%d", n)
}

// Execute closes.
func (t CloseDuplicatePR) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if t.C == nil {
		return Result{}, errors.New("closing pull requests is not available for this agent")
	}
	n, okN := argInt(args, "close")
	keep, okK := argInt(args, "keep")
	if !okN || !okK || n <= 0 || keep <= 0 {
		return Result{}, errors.New("CloseDuplicatePR needs the close and keep pull request numbers")
	}
	reason, _ := argString(args, "reason")
	reason = strings.TrimSpace(strings.Join(strings.Fields(reason), " "))
	if reason == "" {
		return Result{}, errors.New("CloseDuplicatePR needs a reason")
	}
	if r := []rune(reason); len(r) > 500 {
		reason = string(r[:500])
	}
	msg, err := t.C.CloseDuplicatePR(ctx, n, keep, reason)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindPublish, Content: msg}, nil
}

var _ Tool = CloseDuplicatePR{}

func argInt(args map[string]any, key string) (int, bool) {
	n, ok := argInt64(args, key)
	if !ok || n > 1<<31-1 || n < 0 {
		return 0, false
	}
	return int(n), true
}
