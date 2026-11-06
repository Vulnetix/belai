package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/bgagent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/review"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/workdiff"
)

const reviewUsage = `usage: belai review [flags] [BRANCH]

Review a change against a fixed rubric and print structured findings. With no
flag it reviews the working tree's staged, unstaged and untracked changes.
BRANCH (default HEAD) with -base reviews a branch against its merge base with
that ref; -pr reviews a pull request. The model reads the change with read
tools only; the harness checks every finding against the diff. The verdict is
advisory. Exit codes: 0 approve or comment, 1 request_changes, 2 error.

Flags:
  -pr N         review pull request N, or "current" for the open pull request of
                the current branch
  -base REF     review BRANCH (default HEAD) against its merge base with REF
  -json         print the result as JSON
  -comment      with -pr, post the review as a comment on the pull request; this
                needs review.post set to allow in your own settings, and a
                permission rule or hook can still refuse it
  -rubric FILE  a file of extra criteria, one per line (review.rubric_file)
  -provider P   provider for the review turn
  -model M      model for the review turn
  -trust-dir    trust the current directory without prompting
  -force        review again even when a review of this head is stored
`

// reviewArgs are the parsed flags of `belai review`.
type reviewArgs struct {
	pr, base, branch, rubric, provider, model string
	json, comment, trustDir, force            bool
}

func parseReviewArgs(args []string, stderr io.Writer) (reviewArgs, int, bool) {
	var a reviewArgs
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, reviewUsage) }
	fs.StringVar(&a.pr, "pr", "", "review pull request N, or current")
	fs.StringVar(&a.base, "base", "", "review a branch against its merge base with this ref")
	fs.BoolVar(&a.json, "json", false, "print the result as JSON")
	fs.BoolVar(&a.comment, "comment", false, "with -pr, post the review as a pull request comment (needs review.post=allow)")
	fs.StringVar(&a.rubric, "rubric", "", "a file of extra review criteria, one per line")
	fs.StringVar(&a.provider, "provider", "", "provider for the review turn")
	fs.StringVar(&a.model, "model", "", "model for the review turn")
	fs.BoolVar(&a.trustDir, "trust-dir", false, "trust the current directory without prompting")
	fs.BoolVar(&a.force, "force", false, "review again even when a review of this head is stored")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return a, 0, false
		}
		return a, 2, false
	}
	if fs.NArg() > 1 {
		fmt.Fprint(stderr, reviewUsage)
		return a, 2, false
	}
	if fs.NArg() == 1 {
		a.branch = fs.Arg(0)
	}
	return a, 0, true
}

// reviewTarget turns the flags into what to review.
func reviewTarget(a reviewArgs) (review.Target, error) {
	switch {
	case a.pr != "" && (a.base != "" || a.branch != ""):
		return review.Target{}, fmt.Errorf("-pr cannot be combined with -base or a branch")
	case a.pr != "":
		return review.Target{Kind: review.KindPR, Ref: a.pr}, nil
	case a.comment:
		return review.Target{}, fmt.Errorf("-comment needs -pr: only the review of a pull request can be posted")
	case a.base != "" || a.branch != "":
		return review.Target{Kind: review.KindBranch, Ref: a.branch, Base: a.base}, nil
	}
	return review.Target{Kind: review.KindWorkdir}, nil
}

// reviewRun is everything runReview needs, so a test can supply a scripted
// reviewer and forge.
type reviewRun struct {
	opts     review.Options
	target   review.Target
	args     reviewArgs
	dir      string
	guard    review.Guard
	provider func() (forge.Provider, error)
}

// runReview runs one review and prints it. It returns the exit code.
func runReview(ctx context.Context, r reviewRun, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "belai review: reading the change and asking the model; this can take a few minutes")
	res, _, err := review.Run(ctx, r.opts, r.target)
	if err != nil {
		fmt.Fprintln(stderr, "belai review:", sanitize.Line(err.Error(), 400))
		return 2
	}
	if r.args.json {
		out := struct {
			review.Result
			Cached bool `json:"cached"`
		}{res, res.Cached}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(stderr, "belai review:", err)
			return 2
		}
	} else {
		fmt.Fprint(stdout, review.Format(res))
	}
	if r.args.comment {
		if err := postReview(ctx, r, res); err != nil {
			fmt.Fprintln(stderr, "belai review: not posted:", sanitize.Line(err.Error(), 400))
			return 2
		}
		fmt.Fprintln(stderr, "belai review: posted to pull request", res.PR)
	}
	return res.ExitCode()
}

func postReview(ctx context.Context, r reviewRun, res review.Result) error {
	mode := r.opts.Settings.ReviewPostMode()
	if mode != config.ReviewPostAllow {
		return fmt.Errorf("-comment needs review.post set to allow in your own settings (it is %s)", mode)
	}
	p, err := r.provider()
	if err != nil {
		return err
	}
	return review.Post(ctx, review.PostRequest{
		Result: res, Provider: p, Dir: r.dir, Store: r.opts.Store, Guard: r.guard,
		Mode: mode, Unattended: true,
	})
}

// runReviewCLI implements `belai review …` and returns the exit code.
func runReviewCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	a, code, ok := parseReviewArgs(args, stderr)
	if !ok {
		return code
	}
	target, err := reviewTarget(a)
	if err != nil {
		fmt.Fprintln(stderr, "belai review:", err)
		return 2
	}
	workdir, _ := os.Getwd()
	if err := trustRepo(workdir, a.trustDir); err != nil {
		fmt.Fprintln(stderr, "belai review:", err)
		return 2
	}
	settings, pol, err := repoPolicy(workdir)
	if err != nil {
		fmt.Fprintln(stderr, "belai review:", err)
		return 2
	}
	if !settings.ReviewEnabled() {
		fmt.Fprintln(stderr, "belai review:", review.ErrDisabled)
		return 2
	}
	if a.rubric != "" {
		// The user's own flag stands in for review.rubric_file.
		rf := a.rubric
		rs := config.ReviewSettings{}
		if settings.Review != nil {
			rs = *settings.Review
		}
		rs.RubricFile = &rf
		settings.Review = &rs
	}
	resolver, err := newResolver(workdir)
	if err != nil {
		fmt.Fprintln(stderr, "belai review:", err)
		return 2
	}
	cfg, err := run.ResolveWithSource(a.model, a.provider, os.Getenv, resolver)
	if err != nil {
		fmt.Fprintln(stderr, "belai review:", err)
		return 2
	}
	cfg, err = withClassifier(cfg, settings, resolver)
	if err != nil {
		fmt.Fprintln(stderr, "belai review:", err)
		return 2
	}
	mgr := bgagent.NewManager(workdir, cfg, httpclient.Default(), settings, pol)
	mgr.SetCredentialSource(resolver)
	sessionID := session.MustID()
	mgr.SetSessionID(sessionID)
	perms := permissions.From(settings.Permissions.Allow, settings.Permissions.Ask, settings.Permissions.Deny)
	store, _ := review.OpenStore()
	return runReview(ctx, reviewRun{
		opts: review.Options{
			Settings: settings,
			Collector: review.Collector{
				Dir:  workdir,
				Hide: func(rel string) bool { return workdiff.ReadDenied(perms, workdir, rel) },
			},
			Reviewer: review.Agent{Manager: mgr, Workdir: workdir},
			Store:    store,
			Rubric:   review.LoadRubric(settings, workdir),
			Force:    a.force,
		},
		target:   target,
		args:     a,
		dir:      workdir,
		guard:    review.Guard{Perms: perms, Hooks: agent.LoadHooks(settings, pol), SessionID: sessionID, Cwd: workdir},
		provider: func() (forge.Provider, error) { return review.ProviderFor(workdir) },
	}, stdout, stderr)
}
