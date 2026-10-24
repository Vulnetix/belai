package kanban

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// bigPageRemote serves cards in version order but, like a page cut off by the client's response cap,
// fails any page longer than maxPage.
type bigPageRemote struct {
	items   []sessionsync.KanbanItem
	maxPage int
	asked   []int
}

func (r *bigPageRemote) KanbanBatch(context.Context, []sessionsync.KanbanItem) ([]sessionsync.KanbanAck, error) {
	return nil, nil
}

func (r *bigPageRemote) KanbanList(_ context.Context, since int64, limit int) ([]sessionsync.KanbanItem, int64, bool, error) {
	r.asked = append(r.asked, limit)
	if limit > r.maxPage {
		return nil, 0, false, errors.New("unexpected end of JSON input")
	}
	var out []sessionsync.KanbanItem
	for _, it := range r.items {
		if it.Version > since {
			out = append(out, it)
		}
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	cursor := since
	if n := len(out); n > 0 {
		cursor = out[n-1].Version
	}
	return out, cursor, more, nil
}

// A pull whose page cannot be read must not retry the same page for ever: the host's cursor would sit still
// and no card written on the web would arrive (the Software Factory's cards sat in the backlog for a day).
func TestPullFallsBackToASmallerPage(t *testing.T) {
	r := &bigPageRemote{maxPage: 100}
	for i := 1; i <= 260; i++ {
		r.items = append(r.items, sessionsync.KanbanItem{
			ID: fmt.Sprintf("00000000-0000-5000-8000-%012d", i), Title: "t", List: "backlog",
			CreatedAt: 1, UpdatedAt: int64(i), Version: int64(i),
		})
	}
	s := testStore(t)
	NewSyncer(s, r, SyncOptions{}).Sync(context.Background())
	cur, err := s.Cursor()
	if err != nil {
		t.Fatal(err)
	}
	if cur != 260 {
		t.Fatalf("cursor %d after the pull (pages asked: %v), want 260", cur, r.asked)
	}
	got, _ := s.Search(Query{Limit: 1000})
	if len(got) != 260 {
		t.Fatalf("%d cards pulled, want 260", len(got))
	}
}
