package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// webBoard is the website's board, in memory.
type webBoard struct {
	mu      sync.Mutex
	items   map[string]sessionsync.KanbanItem
	version int64
	fail    error
}

func newWebBoard() *webBoard { return &webBoard{items: map[string]sessionsync.KanbanItem{}} }

func (w *webBoard) file(id, title string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.version++
	now := time.Now().UnixMilli()
	w.items[id] = sessionsync.KanbanItem{ID: id, Title: title, List: "backlog", CreatedAt: now, UpdatedAt: now, Version: w.version}
}

func (w *webBoard) has(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.items[id]
	return ok
}

func (w *webBoard) KanbanList(_ context.Context, since int64, _ int) ([]sessionsync.KanbanItem, int64, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail != nil {
		return nil, 0, false, w.fail
	}
	var out []sessionsync.KanbanItem
	for _, it := range w.items {
		if it.Version > since {
			out = append(out, it)
		}
	}
	return out, w.version, false, nil
}

func (w *webBoard) KanbanBatch(_ context.Context, items []sessionsync.KanbanItem) ([]sessionsync.KanbanAck, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail != nil {
		return nil, w.fail
	}
	var acks []sessionsync.KanbanAck
	for _, it := range items {
		w.version++
		it.Version = w.version
		w.items[it.ID] = it
		acks = append(acks, sessionsync.KanbanAck{ID: it.ID, UpdatedAt: it.UpdatedAt, Version: w.version, Applied: true})
	}
	return acks, nil
}

func hostBoard(t *testing.T) *kanban.Store {
	t.Helper()
	return kanban.Open(filepath.Join(t.TempDir(), "kanban.json"))
}

const webCard = "a1b2c3d4-0000-4000-8000-000000000001"

// A web session starts after the card was filed on the website: the card is on the
// session's board before startKanbanSync returns, so the first turn's KanbanSearch
// sees it without a restart.
func TestStartKanbanSyncPullsTheWebsiteBoardBeforeTheFirstTurn(t *testing.T) {
	web := newWebBoard()
	web.file(webCard, "filed on the website")
	board := hostBoard(t)

	stop := startKanbanSync(context.Background(), board, web)
	defer stop()

	got, err := board.Search(kanban.Query{Text: "filed on the website"})
	if err != nil || len(got) != 1 || got[0].ID != webCard {
		t.Fatalf("the board the first turn sees = %+v, %v", got, err)
	}
}

// What the session files is pushed when the session ends, even though the session's
// own context is already cancelled by then (a stop from the website cancels it).
func TestStartKanbanSyncPushesWhatTheSessionFiledOnStop(t *testing.T) {
	web := newWebBoard()
	board := hostBoard(t)
	ctx, cancel := context.WithCancel(context.Background())
	stop := startKanbanSync(ctx, board, web)

	it, _, err := board.Add(kanban.ItemInput{Title: "found while working"},
		kanban.Provenance{SessionID: "s1", HostID: "h1", Project: "p", ProjectKey: "p-1", Dir: "/src/p"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	stop()

	if !web.has(it.ID) {
		t.Fatal("the card the session filed never reached the website")
	}
	if out, _ := board.Outbox(); len(out) != 0 {
		t.Fatalf("outbox not drained: %d", len(out))
	}
}

// An unreachable website must not hold the session up or fail it: the board is
// whatever the host already had.
func TestStartKanbanSyncSurvivesAnUnreachableWebsite(t *testing.T) {
	web := newWebBoard()
	web.fail = errors.New("HTTP 502")
	board := hostBoard(t)
	if _, _, err := board.Add(kanban.ItemInput{Title: "local card"}, kanban.Provenance{SessionID: "s", HostID: "h"}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		stop := startKanbanSync(context.Background(), board, web)
		stop()
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("startKanbanSync did not return against a failing website")
	}
	if got, _ := board.Search(kanban.Query{Text: "local card"}); len(got) != 1 {
		t.Fatalf("the local card was lost: %+v", got)
	}
	if out, _ := board.Outbox(); len(out) != 1 {
		t.Fatalf("an unpushed card must stay in the outbox, have %d", len(out))
	}
}
