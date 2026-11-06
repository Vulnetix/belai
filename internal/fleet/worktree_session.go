package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/trustgate"
)

// The user's worktree commands (/worktree and `belai worktree`, docs/worktrees.md)
// act on worktrees through these functions, so the TUI and the command line
// make the same checks. None takes a path, a ref or an argv from its caller: a
// worktree is named by the id the harness gave it.

// worktreeCardBody is the text of a card filed by NewWorktreeCard: harness
// words, no user text.
const worktreeCardBody = "Opened with /worktree new. A worker that claims this card works in its own git worktree, which stays after the work ends unless worktrees.keep_on_exit is off."

// NewWorktreeCard files the card a board agent claims to work in a worktree.
// The title is the only user text; kanban cleans it. The card carries the
// worktree label, which a worker reads as a request for isolation, and any
// extra labels the caller routes it with.
func NewWorktreeCard(store *kanban.Store, prov kanban.Provenance, title string, labels ...string) (kanban.Item, bool, error) {
	if store == nil {
		return kanban.Item{}, false, errors.New("the kanban board is off")
	}
	return store.Add(kanban.ItemInput{
		Title:  title,
		Body:   worktreeCardBody,
		List:   kanban.Backlog,
		Labels: append([]string{config.WorktreeLabel}, labels...),
	}, prov)
}

// worktreeTrusted refuses to act in a directory the user has not trusted.
func worktreeTrusted(repo string) error {
	st, err := trustgate.Check(repo)
	if err != nil {
		return fmt.Errorf("check trust for %s: %w", repo, err)
	}
	if !st.Trusted {
		return fmt.Errorf("%s is not a trusted workspace; worktree commands run nothing there", repo)
	}
	return nil
}

// worktreeClaimed reports whether a running agent holds the card the worktree
// belongs to. The card may be gone (a deleted card) or never have been one.
func worktreeClaimed(store *kanban.Store, rec WorktreeRecord) bool {
	if store == nil {
		return false
	}
	it, err := store.Get(rec.Item)
	if err != nil {
		return false
	}
	return it.ClaimedBy != "" && it.LeaseUntil > time.Now().UnixMilli()
}

// ErrWorktreeBusy means an agent is working in the worktree.
var ErrWorktreeBusy = errors.New("an agent holds this worktree's card; wait for it to finish, or release the card first")

// UpdateWorktree brings a worktree's branch up to date with its base using
// the user's worktrees.update_mode. It runs no setup or teardown argv.
func UpdateWorktree(ctx context.Context, repo, id string, s config.Settings, store *kanban.Store) (UpdateResult, error) {
	mode := s.WorktreeUpdate()
	if mode == config.WorktreeUpdateOff {
		return UpdateResult{}, ErrUpdateOff
	}
	if err := worktreeTrusted(repo); err != nil {
		return UpdateResult{}, err
	}
	ws, err := OpenWorktree(ctx, repo, id)
	if err != nil {
		return UpdateResult{}, err
	}
	if rec, err := readRecord(ws.repo, id); err == nil && worktreeClaimed(store, rec) {
		return UpdateResult{}, ErrWorktreeBusy
	}
	return ws.UpdateFromBase(ctx, mode)
}

// DropWorktree removes a worktree: the user's teardown argv first, which keep
// it when one fails, then the directory. The branch stays when it holds work.
func DropWorktree(ctx context.Context, repo, id string, s config.Settings, pol posture.Policy, store *kanban.Store) error {
	if err := worktreeTrusted(repo); err != nil {
		return err
	}
	ws, err := OpenWorktree(ctx, repo, id)
	if err != nil {
		return err
	}
	if rec, err := readRecord(ws.repo, id); err == nil && worktreeClaimed(store, rec) {
		return ErrWorktreeBusy
	}
	ws.UseHooks(WorktreeHooks{Settings: s, Posture: pol})
	// A worktree with a record finished its setup.
	ws.ready = true
	return ws.Discard(ctx)
}
