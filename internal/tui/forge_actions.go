package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/clipboard"
	"github.com/vulnetix/belai/internal/forge"
)

// forgeInputKind is the composer prompt a forge action is waiting on.
type forgeInputKind int

const (
	forgeInputNone forgeInputKind = iota
	forgeInputWorktree
	forgeInputPRTitle
	forgeInputPRBody
)

// forgeInputState turns the composer into a one-line prompt for a forge
// action, the same way the ctrl+s save-file flow does.
type forgeInputState struct {
	kind  forgeInputKind
	title string // PR title, carried from the title step to the body step
}

// forgeConfirmKind is the y/N question a forge action is waiting on.
type forgeConfirmKind int

const (
	forgeConfirmNone forgeConfirmKind = iota
	forgeConfirmRemove
	forgeConfirmRemoveForce
	forgeConfirmPush
	forgeConfirmCreatePR
)

// forgeConfirmState is an open y/N question. Anything but y cancels.
type forgeConfirmState struct {
	kind   forgeConfirmKind
	prompt string
	wt     forge.Worktree
	isMain bool
	pr     forge.CreatePRArgs
}

// forgeActionMsg reports a finished forge mutation. next, when set, opens the
// following confirmation (push → create PR).
type forgeActionMsg struct {
	text string
	err  error
	next *forgeConfirmState
}

// forgeFlowActive reports whether a forge prompt or confirmation owns the
// composer.
func (a *App) forgeFlowActive() bool {
	return a.forgeInput.kind != forgeInputNone || a.forgeConfirm.kind != forgeConfirmNone
}

// resetForgeFlows drops any open forge prompt or confirmation.
func (a *App) resetForgeFlows() {
	if a.forgeInput.kind != forgeInputNone {
		a.editor.Reset()
	}
	a.forgeInput = forgeInputState{}
	a.forgeConfirm = forgeConfirmState{}
}

// forgeComposerTitle is the composer title and meta while a forge flow owns
// it; ok is false otherwise.
func (a *App) forgeComposerTitle() (title, meta string, ok bool) {
	if a.forgeConfirm.kind != forgeConfirmNone {
		return a.forgeConfirm.prompt, "y/N · esc cancel", true
	}
	switch a.forgeInput.kind {
	case forgeInputWorktree:
		return "new worktree: branch [path]", "⏎ add · esc cancel", true
	case forgeInputPRTitle:
		return a.forgeNoun() + " title", "⏎ next · esc cancel", true
	case forgeInputPRBody:
		return a.forgeNoun() + " description (optional)", "⏎ review · esc cancel", true
	}
	return "", "", false
}

func (a *App) forgeNoun() string {
	if p := a.forge.snap.Provider; p != nil {
		return p.Noun()
	}
	return "PR"
}

// handleRunsGitKey routes keys on the git tab.
func (a *App) handleRunsGitKey(m tea.KeyMsg) tea.Cmd {
	if m.String() == "r" {
		return a.refreshForge(true)
	}
	if !a.forge.have || a.forge.snap.Root == "" {
		return nil
	}
	s := a.forge.snap
	switch m.String() {
	case "a":
		a.startForgeInput(forgeInputWorktree, "")
	case "enter":
		if wt, ok := a.selectedWorktree(); ok {
			return a.switchWorktree(wt)
		}
	case "x":
		if wt, ok := a.selectedWorktree(); ok {
			a.askRemoveWorktree(wt)
		}
	case "p":
		return a.startCreatePR()
	case "c":
		if s.PR != nil && s.PR.URL != "" {
			a.copyForgeLink(s.PR.URL, s.Provider.Noun()+" URL")
		}
	}
	return nil
}

// handleRunsCIKey routes keys on the ci tab.
func (a *App) handleRunsCIKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "r":
		return a.refreshForge(true)
	case "enter", "c":
		checks := a.forge.snap.Checks
		if a.ciTabVisible() && a.runsSel >= 0 && a.runsSel < len(checks) {
			if link := checks[a.runsSel].Link; link != "" {
				a.copyForgeLink(link, "run link")
			} else {
				a.addSystem("no link for " + checks[a.runsSel].Name)
			}
		}
	}
	return nil
}

func (a *App) copyForgeLink(link, what string) {
	if _, err := clipboard.Copy(link); err != nil {
		a.addSystem("copy failed: " + err.Error())
		return
	}
	a.addSystem("copied " + what + ": " + link)
}

func (a *App) selectedWorktree() (forge.Worktree, bool) {
	wts := a.forge.snap.Worktrees
	if a.runsSel < 0 || a.runsSel >= len(wts) {
		return forge.Worktree{}, false
	}
	return wts[a.runsSel], true
}

// startForgeInput hands the composer to a forge prompt.
func (a *App) startForgeInput(kind forgeInputKind, value string) {
	a.forgeInput.kind = kind
	a.runsFocus = false
	a.editor.Reset()
	a.clearAutocomplete()
	a.editor.SetValue(value)
	a.editor.CursorEnd()
}

// endForgeFlow closes the prompt or confirmation and gives the panel its
// focus back.
func (a *App) endForgeFlow() {
	a.resetForgeFlows()
	a.editor.Reset()
	a.runsFocus = a.runsOpen
}

// handleForgeInputKey routes keys while a forge prompt owns the composer.
func (a *App) handleForgeInputKey(m tea.KeyMsg) tea.Cmd {
	switch m.String() {
	case "esc":
		a.endForgeFlow()
		a.addSystem("cancelled")
		return nil
	case "enter":
	default:
		return a.editor.Update(m)
	}
	value := strings.TrimSpace(a.editor.Value())
	switch a.forgeInput.kind {
	case forgeInputWorktree:
		a.endForgeFlow()
		return a.addWorktree(value)
	case forgeInputPRTitle:
		if value == "" {
			a.addSystem("a title is required")
			return nil
		}
		a.startForgeInput(forgeInputPRBody, "")
		a.forgeInput.title = value
		return nil
	case forgeInputPRBody:
		title := a.forgeInput.title
		a.endForgeFlow()
		a.askCreatePR(forge.CreatePRArgs{Branch: a.forge.snap.Branch, Title: title, Body: value})
		return nil
	}
	return nil
}

// handleForgeConfirmKey answers an open y/N question. Only y proceeds.
func (a *App) handleForgeConfirmKey(m tea.KeyMsg) tea.Cmd {
	c := a.forgeConfirm
	a.endForgeFlow()
	if m.String() != "y" && m.String() != "Y" {
		a.addSystem("cancelled")
		return nil
	}
	s := a.forge.snap
	r := a.forgeRun()
	switch c.kind {
	case forgeConfirmRemove, forgeConfirmRemoveForce:
		force := c.kind == forgeConfirmRemoveForce
		return func() tea.Msg {
			err := forge.RemoveWorktree(context.Background(), r, s.Root, c.wt, c.isMain, force)
			return forgeActionMsg{text: "removed worktree " + displayPath(c.wt.Path), err: err}
		}
	case forgeConfirmPush:
		next := a.createPRConfirm(c.pr)
		return func() tea.Msg {
			err := forge.Push(context.Background(), r, s.Root, c.pr.Branch)
			return forgeActionMsg{text: "pushed " + c.pr.Branch + " to origin", err: err, next: &next}
		}
	case forgeConfirmCreatePR:
		p := s.Provider
		if p == nil {
			return nil
		}
		return func() tea.Msg {
			url, err := p.CreatePR(context.Background(), s.Root, c.pr)
			return forgeActionMsg{text: "created " + p.Noun() + ": " + url, err: err}
		}
	}
	return nil
}

// handleForgeAction reports a finished mutation and refreshes the snapshot.
func (a *App) handleForgeAction(m forgeActionMsg) tea.Cmd {
	if m.err != nil {
		a.addSystem("git: " + forge.CleanErr(m.err))
		return a.refreshForge(true)
	}
	a.addSystem(m.text)
	if m.next != nil {
		a.openForgeConfirm(*m.next)
		return nil
	}
	return a.refreshForge(true)
}

func (a *App) openForgeConfirm(c forgeConfirmState) {
	a.forgeConfirm = c
	a.runsFocus = false
	a.editor.Reset()
	a.clearAutocomplete()
}

// addWorktree parses "branch [path]" and creates the worktree. The path must
// lie inside the session roots; the default is .worktrees/<branch> under the
// repository root.
func (a *App) addWorktree(value string) tea.Cmd {
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > 2 {
		a.addSystem("new worktree: expected a branch and an optional path")
		return nil
	}
	s := a.forge.snap
	branch := fields[0]
	path := filepath.Join(s.Root, ".worktrees", strings.ReplaceAll(branch, "/", "-"))
	if len(fields) == 2 {
		path = expandUserPath(fields[1])
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.Root, path)
		}
	}
	path = filepath.Clean(path)
	if !a.pathInRoots(path) {
		a.addSystem("worktree path " + displayPath(path) + " is outside the session roots; /add-dir its parent first or choose a path under " + displayPath(a.workdir))
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		a.addSystem("worktree path " + displayPath(path) + " already exists")
		return nil
	}
	r := a.forgeRun()
	return func() tea.Msg {
		ctx := context.Background()
		if err := forge.ValidBranchName(ctx, r, s.Root, branch); err != nil {
			return forgeActionMsg{err: err}
		}
		newBranch := !forge.BranchExists(ctx, r, s.Root, branch)
		err := forge.AddWorktree(ctx, r, s.Root, path, branch, newBranch)
		verb := "checked out"
		if newBranch {
			verb = "new branch"
		}
		return forgeActionMsg{text: fmt.Sprintf("added worktree %s (%s %s)", displayPath(path), verb, branch), err: err}
	}
}

// pathInRoots reports whether path lies inside a confinement root. The live
// session's tracker is the authority; before one exists the workdir and the
// adopted workspace directories are the roots.
func (a *App) pathInRoots(path string) bool {
	if a.agent != nil && a.agent.Cwd() != nil {
		return a.agent.Cwd().Contains(path)
	}
	path = filepath.Clean(path)
	if r, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(r, filepath.Base(path))
	}
	for _, root := range append([]string{a.workdir}, a.workspaceDirs...) {
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// switchWorktree moves the session's working directory into wt. The session
// tracker only moves within the primary root, so a worktree elsewhere is
// refused with the way to work there instead.
func (a *App) switchWorktree(wt forge.Worktree) tea.Cmd {
	switch {
	case wt.Current:
		a.addSystem("already in " + displayPath(wt.Path))
		return nil
	case wt.Bare || wt.Prunable:
		a.addSystem("cannot switch to " + displayPath(wt.Path) + ": it has no checkout")
		return nil
	case a.phase != phaseIdle:
		a.addSystem("wait for the turn to finish before switching worktrees")
		return nil
	}
	rel, err := filepath.Rel(a.workdir, wt.Path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		a.addSystem("worktree " + displayPath(wt.Path) + " is outside the session root; start belai there to work in it")
		return nil
	}
	sess, err := a.agentSession()
	if err != nil {
		a.addSystem("switch worktree: " + err.Error())
		return nil
	}
	cwd := sess.Cwd()
	if cwd == nil {
		return nil
	}
	newRel, err := cwd.Change(wt.Path)
	if err != nil {
		a.addSystem("switch worktree: " + err.Error())
		return nil
	}
	a.applyCwd(cwd.Dir(), newRel)
	return tea.Batch(a.refreshGitInfoCmd(), a.refreshForge(true))
}

// askRemoveWorktree opens the removal confirmation, refusing up front what
// forge.RemoveWorktree would refuse anyway.
func (a *App) askRemoveWorktree(wt forge.Worktree) {
	isMain := a.forge.snap.Main(wt)
	switch {
	case wt.Current:
		a.addSystem(forge.ErrRemoveCurrent.Error())
		return
	case isMain || wt.Bare:
		a.addSystem(forge.ErrRemoveMain.Error())
		return
	}
	c := forgeConfirmState{kind: forgeConfirmRemove, wt: wt, isMain: isMain, prompt: "remove worktree " + displayPath(wt.Path) + "?"}
	if wt.Dirty || wt.Locked {
		why := "has uncommitted changes"
		if wt.Locked {
			why = "is locked"
		}
		c.kind = forgeConfirmRemoveForce
		c.prompt = "worktree " + displayPath(wt.Path) + " " + why + " — force remove and lose them?"
	}
	a.openForgeConfirm(c)
}

// startCreatePR begins the PR/MR flow: title, then description, then an
// explicit confirmation naming the forge, repository and branch.
func (a *App) startCreatePR() tea.Cmd {
	s := a.forge.snap
	switch {
	case s.Provider == nil:
		a.addSystem("cannot create a PR: " + s.Reason)
	case s.Branch == "":
		a.addSystem("cannot create a " + s.Provider.Noun() + " from a detached HEAD")
	case s.PR != nil && (s.PR.State == "open" || s.PR.State == "opened"):
		a.addSystem(fmt.Sprintf("%s #%d is already open: %s", s.Provider.Noun(), s.PR.Number, s.PR.URL))
	default:
		a.startForgeInput(forgeInputPRTitle, s.LastSubject)
	}
	return nil
}

// askCreatePR opens the confirmation for a PR/MR. A branch with no upstream
// is pushed first, behind its own confirmation.
func (a *App) askCreatePR(args forge.CreatePRArgs) {
	if a.forge.snap.Upstream == "" {
		a.openForgeConfirm(forgeConfirmState{kind: forgeConfirmPush, pr: args, prompt: "branch " + args.Branch + " has no upstream — push it to origin (git push -u origin " + args.Branch + ")?"})
		return
	}
	a.openForgeConfirm(a.createPRConfirm(args))
}

func (a *App) createPRConfirm(args forge.CreatePRArgs) forgeConfirmState {
	s := a.forge.snap
	repo := s.Remote.Slug()
	if repo == "" {
		repo = s.Remote.URL
	}
	noun := "PR"
	label := ""
	if s.Provider != nil {
		noun, label = s.Provider.Noun(), s.Provider.Label()+" "
	}
	return forgeConfirmState{
		kind:   forgeConfirmCreatePR,
		pr:     args,
		prompt: fmt.Sprintf("create %s%s on %s from %s: %q?", label, noun, repo, args.Branch, args.Title),
	}
}

// expandUserPath expands a leading ~/.
func expandUserPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
