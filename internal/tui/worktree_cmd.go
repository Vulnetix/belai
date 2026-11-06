package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sanitize"
)

// worktreeUsage is /worktree's own help.
const worktreeUsage = "usage: /worktree new TITLE | list | update ID | drop ID  (docs/worktrees.md)"

// worktreeVerbs are the words /worktree completes.
var worktreeVerbs = []string{"new", "list", "update", "drop"}

// worktreeTimeout bounds a confirmed update or drop, which runs the user's
// teardown argv with a timeout each (worktrees.timeout_seconds) on top of git.
func worktreeTimeout(s config.Settings) time.Duration {
	n := 1 + len(s.WorktreeTeardownArgv())
	return time.Duration(n)*time.Duration(s.WorktreeTimeout())*time.Second + 2*time.Minute
}

// worktreeCommand is /worktree. new files a card a board agent works in a
// worktree; list shows the worktrees the harness made; update and drop act on
// one of them after the user confirms, with every argv they would run shown.
func (a *App) worktreeCommand(arg string) tea.Cmd {
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)
	switch verb {
	case "new":
		return a.worktreeNew(rest)
	case "list":
		a.addSystem(a.worktreeList())
	case "update":
		return a.worktreeAskUpdate(rest)
	case "drop":
		return a.worktreeAskDrop(rest)
	default:
		a.addSystem(worktreeUsage)
	}
	return nil
}

func (a *App) worktreeNew(title string) tea.Cmd {
	if title == "" {
		a.addSystem("worktree new: a title is required; " + worktreeUsage)
		return nil
	}
	if a.kb == nil {
		a.addSystem("worktree new: the kanban board is off (the kanban setting is false); a board agent claims the card")
		return nil
	}
	it, dup, err := fleet.NewWorktreeCard(a.kb.store, a.kb.src.Get(), title)
	switch {
	case err != nil:
		a.addSystem("worktree new: " + sanitize.Line(err.Error(), 200))
	case dup:
		a.addSystem(fmt.Sprintf("worktree new: %s is already on the board with that title", it.Short()))
	default:
		a.addSystem(fmt.Sprintf("filed %s with the %s label. Route it to a worker with: belai kanban assign %s PROFILE. A worker that claims it works in its own git worktree, and /worktree list shows it once it exists.", it.Short(), config.WorktreeLabel, it.Short()))
	}
	return nil
}

func (a *App) worktreeList() string {
	recs, err := fleet.WorktreeRecords(a.workdir)
	if err != nil {
		return "worktree list: " + sanitize.Line(err.Error(), 200)
	}
	if len(recs) == 0 {
		return "no worktrees made by the harness for this repository"
	}
	var b strings.Builder
	b.WriteString("worktrees made for this repository:\n")
	for _, r := range recs {
		state := "present"
		if !r.Exists {
			state = "directory gone"
		}
		kept := ""
		if r.Kept {
			kept = ", kept"
		}
		fmt.Fprintf(&b, "  %s  %s  base %s  %s%s\n", r.ID, r.Branch, short12(r.Base), state, kept)
	}
	return strings.TrimRight(b.String(), "\n")
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// worktreeID checks the word names a worktree the harness made.
func (a *App) worktreeID(verb, id string) (fleet.WorktreeRecord, bool) {
	if !fleet.ValidWorktreeID(id) {
		a.addSystem("worktree " + verb + ": " + worktreeUsage)
		return fleet.WorktreeRecord{}, false
	}
	recs, _ := fleet.WorktreeRecords(a.workdir)
	for _, r := range recs {
		if r.ID == id {
			return r, true
		}
	}
	a.addSystem("worktree " + verb + ": no worktree " + id + " for this repository; /worktree list shows them")
	return fleet.WorktreeRecord{}, false
}

func (a *App) worktreeAskUpdate(id string) tea.Cmd {
	rec, ok := a.worktreeID("update", id)
	if !ok {
		return nil
	}
	if a.working() {
		a.addSystem("worktree update: stop the running turn first")
		return nil
	}
	mode := a.settings.WorktreeUpdate()
	if mode == config.WorktreeUpdateOff {
		a.addSystem("worktree update: updating from the base is off; set worktrees.update_mode to merge or rebase in your settings")
		return nil
	}
	if rec.BaseRef == "" {
		a.addSystem("worktree update: " + rec.ID + " was not made from a branch, so there is nothing to update from")
		return nil
	}
	a.openForgeConfirm(forgeConfirmState{
		kind: forgeConfirmWorktreeUpdate, wtID: rec.ID,
		prompt: fmt.Sprintf("%s %s onto %s in %s? A conflict aborts and changes nothing.", mode, rec.Branch, sanitize.Line(rec.BaseRef, 80), rec.ID),
	})
	return nil
}

func (a *App) worktreeAskDrop(id string) tea.Cmd {
	rec, ok := a.worktreeID("drop", id)
	if !ok {
		return nil
	}
	if a.working() {
		a.addSystem("worktree drop: stop the running turn first")
		return nil
	}
	prompt := fmt.Sprintf("remove worktree %s (the branch %s stays if it holds work)?", rec.ID, rec.Branch)
	if td := a.settings.WorktreeTeardownArgv(); len(td) > 0 {
		var lines []string
		for _, argv := range td {
			lines = append(lines, sanitize.Line(strings.Join(argv, " "), 120))
		}
		prompt = fmt.Sprintf("run %s, then remove worktree %s?", strings.Join(lines, "; "), rec.ID)
	}
	a.openForgeConfirm(forgeConfirmState{kind: forgeConfirmWorktreeDrop, wtID: rec.ID, prompt: prompt})
	return nil
}

// worktreeActionCmd runs a confirmed update or drop off the UI loop.
func (a *App) worktreeActionCmd(c forgeConfirmState) tea.Cmd {
	repo, settings, pol := a.workdir, a.settings, a.effectivePosture()
	var board *kanban.Store
	if a.kb != nil {
		board = a.kb.store
	}
	id := c.wtID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout(settings))
		defer cancel()
		if c.kind == forgeConfirmWorktreeUpdate {
			res, err := fleet.UpdateWorktree(ctx, repo, id, settings, board)
			if err != nil {
				return forgeActionMsg{prefix: "worktree", err: worktreeErr(err)}
			}
			return forgeActionMsg{text: id + ": " + res.String()}
		}
		if err := fleet.DropWorktree(ctx, repo, id, settings, pol, board); err != nil {
			return forgeActionMsg{prefix: "worktree", err: worktreeErr(err)}
		}
		return forgeActionMsg{text: "removed worktree " + id}
	}
}

// worktreeErr keeps a hook failure to its harness sentence: the argv's output
// is not shown in the transcript.
func worktreeErr(err error) error {
	var he *fleet.HookError
	if errors.As(err, &he) {
		return errors.New(he.Error() + "; the worktree was kept")
	}
	return err
}
