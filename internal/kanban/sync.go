package kanban

import (
	"context"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// Remote is the backend half of sync; *sessionsync.Client implements it.
type Remote interface {
	KanbanList(ctx context.Context, since int64, limit int) ([]sessionsync.KanbanItem, int64, bool, error)
	KanbanBatch(ctx context.Context, items []sessionsync.KanbanItem) ([]sessionsync.KanbanAck, error)
}

// SyncStatus is a snapshot for /kanban and /sync status.
type SyncStatus struct {
	Running   bool
	LastPush  time.Time
	LastPull  time.Time
	LastError string
	Pending   int
}

// Syncer mirrors the board to the backend. The local file stays the durable
// copy: a local write lands there first, OnChange nudges the syncer, and the
// push happens on the syncer's goroutine, so no tool call or keypress ever
// waits on the network. Pulls run on a timer and on demand.
type Syncer struct {
	store     *Store
	remote    Remote
	pullEvery time.Duration
	onPull    func(changed int)

	nudge chan struct{}
	pull  chan struct{}

	mu     sync.Mutex
	status SyncStatus
	busy   sync.Mutex // one push/pull at a time, Flush included
}

// SyncOptions configures a Syncer. Zero PullEvery is 15s. OnPull, when set,
// runs on the syncer goroutine after a pull that changed the board.
type SyncOptions struct {
	PullEvery time.Duration
	OnPull    func(changed int)
}

// NewSyncer builds a syncer and subscribes it to local changes.
func NewSyncer(store *Store, remote Remote, o SyncOptions) *Syncer {
	if o.PullEvery <= 0 {
		o.PullEvery = 15 * time.Second
	}
	s := &Syncer{
		store: store, remote: remote, pullEvery: o.PullEvery, onPull: o.OnPull,
		nudge: make(chan struct{}, 1), pull: make(chan struct{}, 1),
	}
	store.OnChange(s.Nudge)
	return s
}

// Nudge asks for a push soon. It never blocks.
func (s *Syncer) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// PullNow asks for a pull soon. It never blocks.
func (s *Syncer) PullNow() {
	select {
	case s.pull <- struct{}{}:
	default:
	}
}

// Status returns a snapshot.
func (s *Syncer) Status() SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Start runs the push/pull loop until ctx ends.
func (s *Syncer) Start(ctx context.Context) {
	s.mu.Lock()
	s.status.Running = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.status.Running = false
			s.mu.Unlock()
		}()
		s.cycle(ctx, true)
		t := time.NewTicker(s.pullEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.nudge:
				s.cycle(ctx, false)
			case <-s.pull:
				s.cycle(ctx, true)
			case <-t.C:
				s.cycle(ctx, true)
			}
		}
	}()
}

// Flush pushes the outbox once, for a headless session about to exit.
func (s *Syncer) Flush(ctx context.Context) error {
	s.busy.Lock()
	defer s.busy.Unlock()
	err := s.push(ctx)
	s.note(err, true, false)
	return err
}

func (s *Syncer) cycle(ctx context.Context, pull bool) {
	s.busy.Lock()
	defer s.busy.Unlock()
	err := s.push(ctx)
	s.note(err, true, false)
	if err != nil || !pull {
		return
	}
	s.note(s.pullAll(ctx), false, true)
}

func (s *Syncer) note(err error, pushed, pulled bool) {
	pending := 0
	if out, oerr := s.store.Outbox(); oerr == nil {
		pending = len(out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Pending = pending
	if err != nil {
		s.status.LastError = err.Error()
		return
	}
	s.status.LastError = ""
	now := time.Now()
	if pushed {
		s.status.LastPush = now
	}
	if pulled {
		s.status.LastPull = now
	}
}

func (s *Syncer) push(ctx context.Context) error {
	out, err := s.store.Outbox()
	if err != nil {
		return err
	}
	for len(out) > 0 {
		n := min(len(out), sessionsync.MaxKanbanBatch)
		batch := make([]sessionsync.KanbanItem, n)
		for i, it := range out[:n] {
			batch[i] = ToWire(it)
		}
		acks, err := s.remote.KanbanBatch(ctx, batch)
		if err != nil {
			return err
		}
		pushed := make([]Pushed, 0, len(acks))
		for _, a := range acks {
			pushed = append(pushed, Pushed{ID: a.ID, Updated: a.UpdatedAt, Version: a.Version})
		}
		if err := s.store.MarkPushed(pushed); err != nil {
			return err
		}
		out = out[n:]
	}
	return nil
}

func (s *Syncer) pullAll(ctx context.Context) error {
	cursor, err := s.store.Cursor()
	if err != nil {
		return err
	}
	total := 0
	for range 20 { // bounded: 20 pages of 500 is the whole board many times over
		items, next, more, err := s.remote.KanbanList(ctx, cursor, 500)
		if err != nil {
			return err
		}
		local := make([]Item, 0, len(items))
		for _, w := range items {
			local = append(local, FromWire(w))
		}
		n, err := s.store.Merge(local, next)
		if err != nil {
			return err
		}
		total += n
		if !more || next <= cursor {
			break
		}
		cursor = next
	}
	if total > 0 && s.onPull != nil {
		s.onPull(total)
	}
	return nil
}

// ToWire converts an item to its wire form.
func ToWire(it Item) sessionsync.KanbanItem {
	w := sessionsync.KanbanItem{
		ID: it.ID, Title: it.Title, Body: it.Body, List: string(it.List),
		Project: it.Project, ProjectKey: it.ProjectKey, Cwd: it.Dir,
		HostID: it.HostID, SessionID: it.SessionID,
		CreatedAt: it.Created, UpdatedAt: it.Updated,
		Version: it.ServerVersion, Deleted: it.Deleted,
	}
	for _, m := range it.History {
		w.History = append(w.History, sessionsync.KanbanMove{
			ID: m.ID, From: string(m.From), To: string(m.To), At: m.At, SessionID: m.SessionID, Note: m.Note,
		})
	}
	return w
}

// FromWire converts a wire item. The result is cleaned by Merge.
func FromWire(w sessionsync.KanbanItem) Item {
	it := Item{
		ID: w.ID, Title: w.Title, Body: w.Body, List: List(w.List),
		Project: w.Project, ProjectKey: w.ProjectKey, Dir: w.Cwd,
		HostID: w.HostID, SessionID: w.SessionID,
		Created: w.CreatedAt, Updated: w.UpdatedAt,
		ServerVersion: w.Version, Deleted: w.Deleted,
	}
	for _, m := range w.History {
		it.History = append(it.History, Move{
			ID: m.ID, From: List(m.From), To: List(m.To), At: m.At, SessionID: m.SessionID, Note: m.Note,
		})
	}
	return it
}
