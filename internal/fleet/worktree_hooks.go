package fleet

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/testrun"
)

// Worktree hooks (docs/worktrees.md). The user's worktrees.setup argv run in a
// new worktree before any work starts, and worktrees.teardown argv run before
// it is removed. The harness picks the directory, the timing and the sandbox;
// an argv comes only from the user's own settings layers, never from a model or
// a repository, and runs without a shell.

// MaxSetupOutputPaths bounds the untracked paths a setup may leave in a
// worktree. They are marked as placed so the harness never commits them; more
// than this is a cache or build tree nobody could review, and setup fails.
const MaxSetupOutputPaths = 200000

// hookExcerptRunes caps the output kept from a failed argv.
const hookExcerptRunes = 2000

// WorktreeHooks is what a workspace runs around its life.
type WorktreeHooks struct {
	// Settings supplies the argv lists, the timeout, the network rule, the
	// permission rules and the sandbox settings.
	Settings config.Settings
	// Posture is the effective posture: guardrails off turns the sandbox off.
	Posture posture.Policy
	// Policy, when set, replaces the sandbox policy derived from Settings. For
	// tests.
	Policy func(dir string) sandbox.Policy
	// Run, when set, replaces testrun.Run. For tests.
	Run func(ctx context.Context, opts testrun.Options, name string, argv []string) testrun.Result
}

// HookError is a setup or teardown argv that did not succeed. Error is harness
// text only. The argv's output is arbitrary text: Detail returns a sanitised,
// capped excerpt, and a caller that gives it to a model must classify it as
// tools.KindProcess first.
type HookError struct {
	// Phase is "setup" or "teardown"; Index is the argv's place in its list.
	Phase string
	Index int
	// Program is the base name of argv[0], cleaned.
	Program  string
	Status   testrun.Status
	ExitCode int
	// Note is the runner's harness-composed reason for a run that did not
	// execute (a deny rule, a missing binary, no sandbox).
	Note   string
	output string
}

// Error is a harness-composed sentence: the phase, the program's name and how
// it ended. It never holds output.
func (e *HookError) Error() string {
	how := string(e.Status)
	switch e.Status {
	case testrun.Failed:
		how = fmt.Sprintf("exited %d", e.ExitCode)
	case testrun.TimedOut:
		how = "timed out"
	}
	msg := fmt.Sprintf("worktree %s command %d (%s) %s", e.Phase, e.Index+1, e.Program, how)
	if e.Note != "" && e.Status != testrun.Failed {
		msg += ": " + sanitize.Line(e.Note, 160)
	}
	return msg
}

// Detail is the tail of the argv's output, sanitised and capped. It is raw
// process text: show it to the user, or classify it before a model sees it.
func (e *HookError) Detail() string { return e.output }

// UseHooks gives the workspace the hooks it runs setup and teardown with.
func (w *Workspace) UseHooks(h WorktreeHooks) { w.hooks = &h }

// ID is the name of the worktree's record: its directory's base name.
func (w *Workspace) ID() string { return w.id }

// RunSetup runs worktrees.setup in the worktree, in order, and stops at the
// first failure. It does nothing for a shared workspace or one with no hooks.
// A failure returns a *HookError (or a plain error from the integrity check)
// and leaves the worktree for the caller to remove; teardown then does not run.
// On success the untracked files setup left are marked as placed, so the
// harness never commits them, and teardown is enabled.
func (w *Workspace) RunSetup(ctx context.Context) error {
	if !w.Worktree || w.hooks == nil {
		return nil
	}
	if err := w.intact(); err != nil {
		return err
	}
	list := w.hooks.Settings.WorktreeSetupArgv()
	for i, argv := range list {
		if err := w.hooks.runOne(ctx, w.Dir, "setup", i, argv); err != nil {
			return err
		}
	}
	// The argv ran in the checkout, so it could have rewritten the .git pointer
	// the harness pinned its git calls to.
	if err := w.intact(); err != nil {
		return fmt.Errorf("worktree setup changed the worktree's git file: %w", err)
	}
	if len(list) > 0 {
		if err := w.markSetupOutput(ctx); err != nil {
			return err
		}
	}
	w.ready = true
	return nil
}

// RunTeardown runs worktrees.teardown in the worktree and stops at the first
// failure, which the caller answers by keeping the worktree. It runs only after
// a successful RunSetup, and only while the worktree's git file is the one the
// harness read: a tampered worktree is removed without running the user's argv
// in it.
func (w *Workspace) RunTeardown(ctx context.Context) error {
	if !w.Worktree || w.hooks == nil || !w.ready {
		return nil
	}
	if w.intact() != nil {
		return nil
	}
	for i, argv := range w.hooks.Settings.WorktreeTeardownArgv() {
		if err := w.hooks.runOne(ctx, w.Dir, "teardown", i, argv); err != nil {
			return err
		}
	}
	return nil
}

// sandboxPolicy is the policy one argv runs under: the user's sandbox settings
// with the worktree as the writable root and the network cut unless the user
// allowed it. Where the cut cannot be enforced the sandbox is required, so the
// run is refused rather than quietly given a network.
func (h *WorktreeHooks) sandboxPolicy(dir string) sandbox.Policy {
	if h.Policy != nil {
		return h.Policy(dir)
	}
	p := sandbox.FromSettings(h.Settings.Sandbox, []string{dir}, h.Posture)
	if p.Mode == sandbox.ModeOff {
		return p
	}
	if !h.Settings.WorktreeNetworkAllowed() {
		p.DenyNetwork = true
		if !sandbox.NetworkDenyEnforced() {
			p.Mode = sandbox.ModeRequired
		}
	}
	return p
}

func (h *WorktreeHooks) runOne(ctx context.Context, dir, phase string, idx int, argv []string) error {
	opts := testrun.Options{
		Dir:     dir,
		Timeout: time.Duration(h.Settings.WorktreeTimeout()) * time.Second,
		Perms:   permissions.From(h.Settings.Permissions.Allow, h.Settings.Permissions.Ask, h.Settings.Permissions.Deny),
	}
	run := h.Run
	if run == nil {
		run = testrun.Run
	}
	res := run(sandbox.WithPolicy(ctx, h.sandboxPolicy(dir)), opts, "worktree-"+phase, argv)
	if res.OK() {
		return nil
	}
	return &HookError{
		Phase: phase, Index: idx, Program: sanitize.Ident(filepath.Base(argv[0]), 64),
		Status: res.Status, ExitCode: res.ExitCode, Note: res.Note, output: tailText(res.Output, hookExcerptRunes),
	}
}

// tailText is the last max runes of s, sanitised: the end of a failing
// command's output is where it says why.
func tailText(s string, max int) string {
	s = strings.TrimSpace(sanitize.Text(s))
	if r := []rune(s); len(r) > max {
		s = "…" + string(r[len(r)-max:])
	}
	return s
}

// markSetupOutput records the untracked files setup left behind as placed, so
// changedPaths leaves them out of the harness's commit. Files git ignores are
// not listed by git status in the first place.
func (w *Workspace) markSetupOutput(ctx context.Context) error {
	out, err := w.run(ctx, w.Dir, "git", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("worktree setup: list the files it left: %w", err)
	}
	paths := strings.Split(string(out), "\x00")
	if len(paths) > MaxSetupOutputPaths {
		return fmt.Errorf("worktree setup left more than %d untracked files; add them to .gitignore", MaxSetupOutputPaths)
	}
	if w.placed == nil {
		w.placed = map[string]bool{}
	}
	for _, p := range paths {
		if p != "" {
			w.placed[p] = true
		}
	}
	return nil
}

// AsHookError unwraps err to a *HookError.
func AsHookError(err error) (*HookError, bool) {
	var he *HookError
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}
