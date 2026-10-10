package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PR is the pull request (GitHub) or merge request (GitLab) for a branch.
type PR struct {
	Number int
	Title  string
	State  string // lower-case provider state: open, closed, merged, opened, …
	URL    string
	Draft  bool
	// Branch is the head (source) branch; only listings fill it.
	Branch string
}

// Normalised check states.
const (
	CheckPass    = "pass"
	CheckFail    = "fail"
	CheckPending = "pending"
	CheckSkipped = "skipped"
	CheckCancel  = "cancelled"
)

// Check is one CI check (GitHub) or pipeline job (GitLab).
type Check struct {
	Name     string
	Workflow string // workflow (GitHub) or stage (GitLab)
	State    string // one of the Check* constants
	Link     string
}

// CreatePRArgs are the inputs to CreatePR.
type CreatePRArgs struct {
	Branch string
	Title  string
	Body   string
	// Draft opens the pull or merge request as a draft.
	Draft bool
}

// Provider drives one forge's CLI.
type Provider interface {
	// Kind is KindGitHub or KindGitLab.
	Kind() string
	// Noun is "PR" or "MR".
	Noun() string
	// Label is the display name: "GitHub (gh)".
	Label() string
	// PRForBranch returns the PR/MR for branch, or nil when there is none.
	PRForBranch(ctx context.Context, dir, branch string) (*PR, error)
	// CreatePR opens a PR/MR and returns its URL.
	CreatePR(ctx context.Context, dir string, args CreatePRArgs) (string, error)
	// Checks lists the CI checks for pr on branch.
	Checks(ctx context.Context, dir, branch string, pr PR) ([]Check, error)
	// OpenPRsWithPrefix lists the open PRs/MRs whose head branch starts with
	// prefix and lives in the same repository: a fork can name its branch
	// anything, so a fork's PR is never one of ours.
	OpenPRsWithPrefix(ctx context.Context, dir, prefix string) ([]PR, error)
	// ClosePR closes the open PR/MR number, leaving comment on it first.
	ClosePR(ctx context.Context, dir string, number int, comment string) error
}

// withPrefix keeps the PRs whose head branch starts with prefix.
func withPrefix(prs []PR, prefix string) []PR {
	var out []PR
	for _, p := range prs {
		if prefix != "" && strings.HasPrefix(p.Branch, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// For picks the provider for rem. When none applies, reason says why in a
// form fit for the git tab: a generic remote, or the CLI missing from PATH.
func For(rem Remote, r Runner, look LookPath) (Provider, string) {
	switch rem.Kind {
	case KindGitHub:
		if _, err := look("gh"); err != nil {
			return nil, "gh not installed — PR and CI actions need the GitHub CLI (https://cli.github.com)"
		}
		return github{r: r}, ""
	case KindGitLab:
		if _, err := look("glab"); err != nil {
			return nil, "glab not installed — MR and CI actions need the GitLab CLI (https://gitlab.com/gitlab-org/cli)"
		}
		return gitlab{r: r}, ""
	}
	if rem.Host == "" {
		return nil, "no origin remote"
	}
	return nil, "no PR/CI support for " + rem.Host
}

// github drives gh.
type github struct{ r Runner }

func (github) Kind() string  { return KindGitHub }
func (github) Noun() string  { return "PR" }
func (github) Label() string { return "GitHub (gh)" }

func (g github) PRForBranch(ctx context.Context, dir, branch string) (*PR, error) {
	if err := argSafe(branch); err != nil {
		return nil, err
	}
	out, err := run(ctx, g.r, ReadTimeout, dir, "gh", "pr", "view", branch, "--json", "number,title,state,url,isDraft")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no pull requests found") {
			return nil, nil
		}
		return nil, err
	}
	return parseGitHubPR(out)
}

func parseGitHubPR(out string) (*PR, error) {
	var v struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		URL     string `json:"url"`
		IsDraft bool   `json:"isDraft"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("gh: unreadable pr view output")
	}
	if v.Number == 0 {
		return nil, nil
	}
	return &PR{Number: v.Number, Title: Clean(v.Title), State: strings.ToLower(Clean(v.State)), URL: Clean(v.URL), Draft: v.IsDraft}, nil
}

func (g github) CreatePR(ctx context.Context, dir string, a CreatePRArgs) (string, error) {
	if err := argSafe(a.Branch); err != nil {
		return "", err
	}
	argv := []string{"gh", "pr", "create", "--head", a.Branch, "--title", a.Title, "--body", a.Body}
	if a.Draft {
		argv = append(argv, "--draft")
	}
	out, err := run(ctx, g.r, WriteTimeout, dir, argv...)
	if err != nil {
		return "", err
	}
	return Clean(lastLine(out)), nil
}

func (g github) OpenPRsWithPrefix(ctx context.Context, dir, prefix string) ([]PR, error) {
	if err := argSafe(prefix); err != nil {
		return nil, err
	}
	out, err := run(ctx, g.r, WriteTimeout, dir, "gh", "pr", "list", "--state", "open", "--limit", "200", "--json", "number,title,state,url,isDraft,headRefName,isCrossRepository")
	if err != nil {
		return nil, err
	}
	return parseGitHubPRList(out, prefix)
}

func parseGitHubPRList(out, prefix string) ([]PR, error) {
	var rows []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		URL     string `json:"url"`
		IsDraft bool   `json:"isDraft"`
		Head    string `json:"headRefName"`
		Fork    bool   `json:"isCrossRepository"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("gh: unreadable pr list output")
	}
	prs := make([]PR, 0, len(rows))
	for _, r := range rows {
		if r.Fork {
			continue
		}
		prs = append(prs, PR{Number: r.Number, Title: Clean(r.Title), State: strings.ToLower(Clean(r.State)), URL: Clean(r.URL), Draft: r.IsDraft, Branch: Clean(r.Head)})
	}
	return withPrefix(prs, prefix), nil
}

func (g github) ClosePR(ctx context.Context, dir string, number int, comment string) error {
	if number <= 0 {
		return errors.New("no pull request number")
	}
	_, err := run(ctx, g.r, WriteTimeout, dir, "gh", "pr", "close", strconv.Itoa(number), "--comment", comment)
	return err
}

func (g github) Checks(ctx context.Context, dir, _ string, pr PR) ([]Check, error) {
	out, err := run(ctx, g.r, ReadTimeout, dir, "gh", "pr", "checks", strconv.Itoa(pr.Number), "--json", "name,state,bucket,link,workflow")
	// gh exits non-zero when a check failed (1) or is pending (8) but still
	// prints the list, so parse whenever there is output.
	if out == "" {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "no checks reported") {
			return nil, nil
		}
		return nil, err
	}
	return parseGitHubChecks(out)
}

func parseGitHubChecks(out string) ([]Check, error) {
	var rows []struct {
		Name     string `json:"name"`
		State    string `json:"state"`
		Bucket   string `json:"bucket"`
		Link     string `json:"link"`
		Workflow string `json:"workflow"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("gh: unreadable pr checks output")
	}
	checks := make([]Check, 0, len(rows))
	for _, r := range rows {
		state := r.Bucket
		if state == "" {
			state = r.State
		}
		checks = append(checks, Check{Name: Clean(r.Name), Workflow: Clean(r.Workflow), State: normState(state), Link: Clean(r.Link)})
	}
	return checks, nil
}

// gitlab drives glab.
type gitlab struct{ r Runner }

func (gitlab) Kind() string  { return KindGitLab }
func (gitlab) Noun() string  { return "MR" }
func (gitlab) Label() string { return "GitLab (glab)" }

func (g gitlab) PRForBranch(ctx context.Context, dir, branch string) (*PR, error) {
	if err := argSafe(branch); err != nil {
		return nil, err
	}
	out, err := run(ctx, g.r, ReadTimeout, dir, "glab", "mr", "view", branch, "-F", "json")
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "no open merge request") || strings.Contains(low, "no merge request") || strings.Contains(low, "404") {
			return nil, nil
		}
		return nil, err
	}
	return parseGitLabMR(out)
}

func parseGitLabMR(out string) (*PR, error) {
	var v struct {
		IID    int    `json:"iid"`
		Title  string `json:"title"`
		State  string `json:"state"`
		WebURL string `json:"web_url"`
		Draft  bool   `json:"draft"`
		WIP    bool   `json:"work_in_progress"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("glab: unreadable mr view output")
	}
	if v.IID == 0 {
		return nil, nil
	}
	return &PR{Number: v.IID, Title: Clean(v.Title), State: strings.ToLower(Clean(v.State)), URL: Clean(v.WebURL), Draft: v.Draft || v.WIP}, nil
}

func (g gitlab) CreatePR(ctx context.Context, dir string, a CreatePRArgs) (string, error) {
	if err := argSafe(a.Branch); err != nil {
		return "", err
	}
	argv := []string{"glab", "mr", "create", "--source-branch", a.Branch, "--title", a.Title, "--description", a.Body, "--yes"}
	if a.Draft {
		argv = append(argv, "--draft")
	}
	out, err := run(ctx, g.r, WriteTimeout, dir, argv...)
	if err != nil {
		return "", err
	}
	return Clean(lastLine(out)), nil
}

func (g gitlab) OpenPRsWithPrefix(ctx context.Context, dir, prefix string) ([]PR, error) {
	if err := argSafe(prefix); err != nil {
		return nil, err
	}
	out, err := run(ctx, g.r, WriteTimeout, dir, "glab", "mr", "list", "--per-page", "100", "-F", "json")
	if err != nil {
		return nil, err
	}
	return parseGitLabMRList(out, prefix)
}

func parseGitLabMRList(out, prefix string) ([]PR, error) {
	var rows []struct {
		IID    int    `json:"iid"`
		Title  string `json:"title"`
		State  string `json:"state"`
		WebURL string `json:"web_url"`
		Draft  bool   `json:"draft"`
		Source string `json:"source_branch"`
		From   int    `json:"source_project_id"`
		To     int    `json:"target_project_id"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("glab: unreadable mr list output")
	}
	prs := make([]PR, 0, len(rows))
	for _, r := range rows {
		if st := strings.ToLower(r.State); (st != "opened" && st != "open") || r.From != r.To {
			continue
		}
		prs = append(prs, PR{Number: r.IID, Title: Clean(r.Title), State: strings.ToLower(Clean(r.State)), URL: Clean(r.WebURL), Draft: r.Draft, Branch: Clean(r.Source)})
	}
	return withPrefix(prs, prefix), nil
}

func (g gitlab) ClosePR(ctx context.Context, dir string, number int, comment string) error {
	if number <= 0 {
		return errors.New("no merge request number")
	}
	if _, err := run(ctx, g.r, WriteTimeout, dir, "glab", "mr", "note", strconv.Itoa(number), "-m", comment); err != nil {
		return err
	}
	_, err := run(ctx, g.r, WriteTimeout, dir, "glab", "mr", "close", strconv.Itoa(number))
	return err
}

func (g gitlab) Checks(ctx context.Context, dir, branch string, _ PR) ([]Check, error) {
	if err := argSafe(branch); err != nil {
		return nil, err
	}
	out, err := run(ctx, g.r, ReadTimeout, dir, "glab", "ci", "get", "-b", branch, "-F", "json")
	if err != nil {
		return nil, err
	}
	return parseGitLabJobs(out)
}

func parseGitLabJobs(out string) ([]Check, error) {
	var v struct {
		Jobs []struct {
			Name   string `json:"name"`
			Stage  string `json:"stage"`
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("glab: unreadable ci get output")
	}
	checks := make([]Check, 0, len(v.Jobs))
	for _, j := range v.Jobs {
		checks = append(checks, Check{Name: Clean(j.Name), Workflow: Clean(j.Stage), State: normState(j.Status), Link: Clean(j.WebURL)})
	}
	return checks, nil
}

// normState folds GitHub buckets/states and GitLab job statuses onto the
// Check* constants. Anything unrecognised is pending: it has not passed.
func normState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pass", "success", "successful", "neutral":
		return CheckPass
	case "fail", "failure", "failed", "error", "timed_out", "action_required", "startup_failure":
		return CheckFail
	case "skipping", "skipped", "manual":
		return CheckSkipped
	case "cancel", "cancelled", "canceled":
		return CheckCancel
	}
	return CheckPending
}
