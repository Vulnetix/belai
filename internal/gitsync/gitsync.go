// Package gitsync keeps a session's repository current with its upstream
// default branch, and describes the repository for the website.
//
// Before a session's first turn, and again before the first turn that follows
// a commit, Hygiene fetches origin and rebases the checked-out branch onto
// origin's default branch, so work does not start from a base that has moved
// on and collide with it later. On the default branch itself that is a plain
// pull --rebase; on a feature branch, a worktree or a checked-out PR it brings
// the newest upstream main into the branch.
//
// It only ever acts on a quiet repository: no uncommitted tracked changes, no
// rebase, merge, cherry-pick, revert or bisect in progress, a branch (not a
// detached HEAD) and an origin to fetch from. It never stashes, resets or
// discards anything. A rebase that conflicts is aborted at once, leaving the
// branch exactly where it was, and the result says so. Every call is argv-only
// through forge.Runner, bounded by a timeout, and cannot prompt.
//
// Info is what the website shows for the session: branch, worktree, how far
// the branch is from upstream and from main, the pull or merge request through
// the provider's own CLI, and the last sync. Nothing here enters a model turn.
package gitsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/forge"
)

// Outcomes of one sync.
const (
	// OutcomeSynced: the branch was rebased onto the base and moved.
	OutcomeSynced = "synced"
	// OutcomeCurrent: the branch already contains the base; nothing moved.
	OutcomeCurrent = "current"
	// OutcomeSkipped: the repository was not in a state it is safe to rebase.
	OutcomeSkipped = "skipped"
	// OutcomeConflict: the rebase conflicted and was aborted; nothing moved.
	OutcomeConflict = "conflict"
	// OutcomeError: a git call failed (offline, no access, timed out).
	OutcomeError = "error"
)

// Result is one sync attempt, as the transcript and the website show it.
type Result struct {
	Outcome string `json:"outcome"`
	// Reason says why a sync was skipped, conflicted or failed.
	Reason string `json:"reason,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Base is the ref the branch was rebased onto, e.g. origin/main.
	Base string `json:"base,omitempty"`
	// Pulled is how many base commits the branch was missing.
	Pulled int `json:"pulled,omitempty"`
	// From and To are the short HEAD before and after a synced rebase. To get
	// back: git reset --hard <From> (or git reflog).
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// NeedsPush is set when the rebase rewrote commits the branch's upstream
	// already has: the remote branch (and its PR) updates only on a force push.
	NeedsPush bool  `json:"needsPush,omitempty"`
	At        int64 `json:"at"`
}

// Line is the one-line account of the result for a transcript or a terminal.
func (r Result) Line() string {
	switch r.Outcome {
	case OutcomeSynced:
		s := fmt.Sprintf("git sync: rebased %s onto %s (%s new, %s to %s)", r.Branch, r.Base, plural(r.Pulled, "commit"), r.From, r.To)
		if r.NeedsPush {
			s += "; the branch is rewritten, so update its remote with git push --force-with-lease"
		}
		return s
	case OutcomeCurrent:
		return fmt.Sprintf("git sync: %s already has everything in %s", r.Branch, r.Base)
	case OutcomeConflict:
		return fmt.Sprintf("git sync: %s conflicts with %s (%s); rebase aborted, branch unchanged", r.Branch, r.Base, r.Reason)
	case OutcomeError:
		return "git sync failed: " + r.Reason
	default:
		return "git sync skipped: " + r.Reason
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// Hygiene runs the sync for one session. Safe for concurrent use.
type Hygiene struct {
	dir string
	run forge.Runner
	now func() time.Time

	mu       sync.Mutex
	enabled  bool
	ran      bool
	lastHead string
	reported string
	last     *Result
	info     *Info
	infoAt   time.Time
	kick     chan struct{}
	raw      json.RawMessage
}

// New returns the hygiene for the repository containing dir. A nil run uses
// forge.NonInteractiveRunner.
func New(dir string, enabled bool, run forge.Runner) *Hygiene {
	if run == nil {
		run = forge.NonInteractiveRunner
	}
	return &Hygiene{dir: dir, run: run, now: time.Now, enabled: enabled, kick: make(chan struct{}, 1)}
}

// Enabled reports whether the sync runs before turns.
func (h *Hygiene) Enabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enabled
}

// SetEnabled turns the sync on or off for the rest of the session.
func (h *Hygiene) SetEnabled(on bool) {
	h.mu.Lock()
	h.enabled = on
	h.mu.Unlock()
}

// Last is the most recent attempt, or nil before one.
func (h *Hygiene) Last() *Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

// BeforeTurn syncs when it is due and returns what happened, or nil when
// there is nothing to say: the sync is off, dir is not a repository, no commit
// has landed since the last sync, or the same skip was already reported.
//
// It is due on the session's first turn, and on the first turn after HEAD
// moved (a commit). A skipped sync does not use up the first turn: it is tried
// again at the next turn start, when the tree may be clean.
func (h *Hygiene) BeforeTurn(ctx context.Context) *Result {
	h.mu.Lock()
	enabled, ran, lastHead := h.enabled, h.ran, h.lastHead
	h.mu.Unlock()
	if !enabled {
		return nil
	}
	head, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "HEAD")
	if err != nil {
		// Not a repository, or one with no commits yet: nothing to keep current.
		return nil
	}
	if ran && head == lastHead {
		return nil
	}
	res := h.Sync(ctx)

	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = &res
	h.info = nil
	if res.Outcome != OutcomeSkipped {
		h.ran = true
	}
	if after, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "HEAD"); err == nil && res.Outcome != OutcomeSkipped {
		h.lastHead = after
	}
	// A skip repeats every turn until the tree is clean: say it once.
	if res.Outcome == OutcomeSkipped {
		if line := res.Line(); line == h.reported {
			return nil
		} else {
			h.reported = line
		}
	} else {
		h.reported = ""
	}
	return &res
}

// Sync fetches origin and rebases the current branch onto origin's default
// branch, if the repository is in a state where that is safe.
func (h *Hygiene) Sync(ctx context.Context) Result {
	res := Result{At: h.now().UnixMilli()}
	skip := func(reason string) Result {
		res.Outcome, res.Reason = OutcomeSkipped, reason

		return res
	}
	fail := func(outcome, reason string) Result {
		res.Outcome, res.Reason = outcome, forge.Clean(reason)

		return res
	}

	gitDir, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return skip("not a git repository")
	}
	res.Branch, _ = h.git(ctx, forge.ReadTimeout, "branch", "--show-current")
	if res.Branch == "" {
		return skip("detached HEAD")
	}
	if op := inProgress(gitDir); op != "" {
		return skip(op + " in progress")
	}
	if dirty, err := h.git(ctx, forge.ReadTimeout, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return fail(OutcomeError, err.Error())
	} else if dirty != "" {
		return skip("uncommitted changes")
	}
	if _, err := h.git(ctx, forge.ReadTimeout, "remote", "get-url", "origin"); err != nil {
		return skip("no origin remote")
	}
	if _, err := h.git(ctx, forge.WriteTimeout, "fetch", "--prune", "origin"); err != nil {
		return fail(OutcomeError, "fetch origin: "+err.Error())
	}
	base := h.defaultBase(ctx)
	if base == "" {
		return skip("cannot tell origin's default branch")
	}
	res.Base = base

	behind, err := h.git(ctx, forge.ReadTimeout, "rev-list", "--count", "HEAD.."+base)
	if err != nil {
		return fail(OutcomeError, err.Error())
	}
	res.Pulled, _ = strconv.Atoi(behind)
	if res.Pulled == 0 {
		res.Outcome = OutcomeCurrent

		return res
	}
	before, _ := h.git(ctx, forge.ReadTimeout, "rev-parse", "HEAD")
	res.From = short(before)

	if _, err := h.git(ctx, forge.WriteTimeout, "-c", "rebase.autoStash=false", "rebase", "--rebase-merges", base); err != nil {
		reason := err.Error()
		if inProgress(gitDir) != "" {
			if _, aerr := h.git(ctx, forge.WriteTimeout, "rebase", "--abort"); aerr != nil {
				return fail(OutcomeError, "rebase stopped and could not be aborted, run git rebase --abort: "+aerr.Error())
			}
			return fail(OutcomeConflict, reason)
		}
		return fail(OutcomeError, reason)
	}
	after, _ := h.git(ctx, forge.ReadTimeout, "rev-parse", "HEAD")
	res.To = short(after)
	res.Outcome = OutcomeSynced
	res.NeedsPush = after != before && h.diverged(ctx)

	return res
}

// defaultBase is origin's default branch as a remote-tracking ref, e.g.
// origin/main: what origin/HEAD says, else origin/main, else origin/master.
func (h *Hygiene) defaultBase(ctx context.Context) string {
	if ref, err := h.git(ctx, forge.ReadTimeout, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && strings.HasPrefix(ref, "origin/") {
		if _, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return ref
		}
	}
	for _, b := range []string{"origin/main", "origin/master"} {
		if _, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "--verify", "--quiet", b); err == nil {
			return b
		}
	}
	return ""
}

// diverged reports whether the branch has an upstream that is no longer an
// ancestor of HEAD: after a rebase, the remote branch (and its PR) can only be
// updated by a force push.
func (h *Hygiene) diverged(ctx context.Context) bool {
	if _, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "--verify", "--quiet", "@{u}"); err != nil {
		return false
	}
	_, err := h.git(ctx, forge.ReadTimeout, "merge-base", "--is-ancestor", "@{u}", "HEAD")

	return err != nil
}

// git runs one git command in the session's directory. This is harness code
// running outside the agent's sandbox, so repository config that would run a
// program on its own (the filesystem monitor, hooks) is switched off for it.
func (h *Hygiene) git(ctx context.Context, timeout time.Duration, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append([]string{"git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull}, argv...)
	out, err := h.run(ctx, h.dir, full...)

	return strings.TrimSpace(string(out)), err
}

// Raw is the last reading Watch took, as the JSON the website receives, or nil
// before one (or outside a repository). Cheap: it returns a cached value, so
// the syncer can poll it on every tick.
func (h *Hygiene) Raw() json.RawMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.raw
}

// Kick asks Watch for a fresh reading now: a turn ended, a sync ran, the
// switch changed.
func (h *Hygiene) Kick() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// Watch reads Info now, every `every`, and on each Kick, and calls push when
// what the website would show has changed. It returns when ctx ends. A
// directory outside a repository pushes nothing.
func (h *Hygiene) Watch(ctx context.Context, every time.Duration, push func(*Info)) {
	var last string
	read := func(force bool) {
		in := h.Info(ctx, force)
		if in == nil {
			return
		}
		cmp := *in
		cmp.At = 0
		b, err := json.Marshal(cmp)
		if err != nil || string(b) == last {
			return
		}
		last = string(b)
		h.mu.Lock()
		h.raw = b
		h.mu.Unlock()
		if push != nil {
			push(in)
		}
	}
	read(true)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			read(false)
		case <-h.kick:
			read(true)
		}
	}
}

// inProgress names the multi-step git operation underway in gitDir, or "".
func inProgress(gitDir string) string {
	for _, c := range []struct{ file, name string }{
		{"rebase-merge", "a rebase"}, {"rebase-apply", "a rebase"}, {"MERGE_HEAD", "a merge"},
		{"CHERRY_PICK_HEAD", "a cherry-pick"}, {"REVERT_HEAD", "a revert"}, {"BISECT_LOG", "a bisect"},
	} {
		if _, err := os.Stat(filepath.Join(gitDir, c.file)); err == nil {
			return c.name
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
