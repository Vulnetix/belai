// Package kanban owns the global kanban board every Belai session shares:
// one binary file under the global state directory holding the items of five
// lists (backlog, review, in_progress, done, blocked).
//
// The board is bookkeeping, not workspace state. Models write to it through
// the Kanban* tools, the user through /kanban and the composer pane, and the
// Vulnetix website through session sync; every write lands in the local file
// first, which is always the durable copy, and is pushed to the backend by the
// sync worker afterwards.
//
// Everything stored here is text some model, or some web user, wrote. It is
// cleaned on the way in (delimiter markup, control and bidi runes stripped,
// capped) and it is still untrusted on the way out: the tools that read it
// classify. Provenance — which session, host, project and directory added an
// item — is stamped by the harness and is never taken from a model argument.
package kanban

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/session"
)

// List is one of the five board lists.
type List string

const (
	Backlog    List = "backlog"
	Review     List = "review"
	InProgress List = "in_progress"
	Done       List = "done"
	Blocked    List = "blocked"
)

// Lists is every list in board order.
var Lists = []List{Backlog, Review, InProgress, Blocked, Done}

// Valid reports whether l is one of the five lists.
func (l List) Valid() bool { return slices.Contains(Lists, l) }

// ParseList maps a list name, and the spellings models reach for, onto a
// List. ok is false for anything else.
func ParseList(s string) (List, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.NewReplacer("-", "_", " ", "_").Replace(s))) {
	case "backlog", "todo", "to_do":
		return Backlog, true
	case "review", "triage":
		return Review, true
	case "in_progress", "inprogress", "doing", "active", "started", "wip":
		return InProgress, true
	case "done", "complete", "completed", "closed", "finished":
		return Done, true
	case "blocked", "stuck", "waiting":
		return Blocked, true
	}
	return "", false
}

// Limits. Every text field is capped when it is written, whoever writes it.
const (
	MaxTitleRunes = 200
	MaxBodyBytes  = 4 << 10
	MaxNoteBytes  = 1 << 10
	MaxItems      = 5000
	// MaxHistory is how many moves and notes an item keeps; the oldest drop.
	MaxHistory = 50
	// tombstoneTTL is how long a deleted item is kept for a sync that has not
	// pushed it yet. A board that never syncs still sheds them.
	tombstoneTTL = 30 * 24 * time.Hour
)

// Move is one entry in an item's history: a move between lists, or a note
// (From == To).
type Move struct {
	ID        string
	From, To  List
	At        int64 // unix ms
	SessionID string
	Note      string
}

// Item is one card on the board.
type Item struct {
	ID    string // UUIDv4, unique across hosts
	Title string
	Body  string
	List  List
	// Provenance, stamped by the harness when the item was added.
	Project    string
	ProjectKey string
	Dir        string
	HostID     string
	SessionID  string

	Created int64 // unix ms
	Updated int64 // unix ms
	History []Move

	// Sync state. ServerVersion is the backend's version of the item (0 when
	// never pushed); Dirty marks a local change not yet pushed; Deleted is a
	// tombstone kept until the delete is pushed.
	ServerVersion int64
	Dirty         bool
	Deleted       bool
}

// Short returns the short id models and users quote: "K-" and the first six
// hex digits of the uuid.
func (it Item) Short() string { return ShortID(it.ID) }

// ShortID returns the short form of a uuid.
func ShortID(id string) string {
	hex := strings.ReplaceAll(strings.ToLower(id), "-", "")
	if len(hex) > 6 {
		hex = hex[:6]
	}
	return "K-" + hex
}

// LastNote returns the most recent history note, or "".
func (it Item) LastNote() string {
	for i := len(it.History) - 1; i >= 0; i-- {
		if it.History[i].Note != "" {
			return it.History[i].Note
		}
	}
	return ""
}

// Board is the whole persisted board.
type Board struct {
	// Cursor is the highest backend version pulled so far.
	Cursor int64
	Items  []Item
}

// Provenance is who is writing: stamped on items and moves by the harness.
type Provenance struct {
	SessionID  string
	HostID     string
	Project    string
	ProjectKey string
	Dir        string
}

// ItemInput is a new item's content.
type ItemInput struct {
	Title string
	Body  string
	List  List
}

// Patch edits an item. Nil fields are left alone; a non-empty Note is
// appended to the history.
type Patch struct {
	Title *string
	Body  *string
	Note  string
}

// Query selects items.
type Query struct {
	Text  string
	Lists []List
	// Project matches Item.Project or Item.ProjectKey; empty means every
	// project.
	Project string
	Limit   int
}

// Errors.
var (
	ErrNotFound  = errors.New("kanban: no such item")
	ErrCorrupt   = errors.New("kanban: the board file is unreadable")
	ErrFull      = fmt.Errorf("kanban: the board holds the maximum of %d items; move or delete some first", MaxItems)
	ErrNoTitle   = errors.New("kanban: an item needs a title")
	ErrBadList   = errors.New("kanban: list must be one of backlog, review, in_progress, blocked, done")
	ErrAmbiguous = errors.New("kanban: the id matches more than one item")
)

// Store is the board file plus an in-memory copy that reloads when the file
// changes. It is safe for concurrent use, and several processes may hold a
// Store over the same file: every mutation is a locked read-modify-write.
type Store struct {
	path string

	mu      sync.Mutex
	board   Board
	stamp   fileStamp
	loaded  bool
	loadErr error

	lmu       sync.Mutex
	listeners []func()

	now func() time.Time
}

type fileStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

// Open returns a Store over path. It does no I/O until first use.
func Open(path string) *Store {
	return &Store{path: path, now: time.Now}
}

// OpenDefault opens the board at config.KanbanPath.
func OpenDefault() (*Store, error) {
	p, err := config.KanbanPath()
	if err != nil {
		return nil, err
	}
	return Open(p), nil
}

// Path returns the board file path.
func (s *Store) Path() string { return s.path }

// OnChange registers fn to run after every local mutation (not after a sync
// merge). It runs on the mutating goroutine, outside the store's lock.
func (s *Store) OnChange(fn func()) {
	s.lmu.Lock()
	s.listeners = append(s.listeners, fn)
	s.lmu.Unlock()
}

func (s *Store) notify() {
	s.lmu.Lock()
	fns := slices.Clone(s.listeners)
	s.lmu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (s *Store) nowMs() int64 { return s.now().UnixMilli() }

func stat(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileStamp{}, nil
	}
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size(), ok: true}, nil
}

// refreshLocked reloads the board when the file changed since the last load.
// A missing file is an empty board; an unreadable one is an error that sticks
// until the file changes, and nothing is ever written over it.
func (s *Store) refreshLocked() error {
	st, err := stat(s.path)
	if err != nil {
		return err
	}
	if s.loaded && st == s.stamp {
		return s.loadErr
	}
	s.loaded, s.stamp = true, st
	if !st.ok {
		s.board, s.loadErr = Board{}, nil
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		s.loadErr = err
		return err
	}
	b, err := Decode(data)
	if err != nil {
		s.loadErr = fmt.Errorf("%w (%s): %v", ErrCorrupt, s.path, err)
		return s.loadErr
	}
	s.board, s.loadErr = b, nil
	return nil
}

// read runs fn over a current snapshot of the board.
func (s *Store) read(fn func(b *Board)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshLocked(); err != nil {
		return err
	}
	fn(&s.board)
	return nil
}

// mutate is the single write path: take the cross-process lock, reload, apply
// fn, write atomically. fn's error aborts the write.
func (s *Store) mutate(local bool, fn func(b *Board) error) error {
	s.mu.Lock()
	err := s.mutateLocked(fn)
	s.mu.Unlock()
	if err == nil && local {
		s.notify()
	}
	return err
}

func (s *Store) mutateLocked(fn func(b *Board) error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	release, err := config.AcquireFileLock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	if err := s.refreshLocked(); err != nil {
		return err
	}
	work := cloneBoard(s.board)
	if err := fn(&work); err != nil {
		return err
	}
	pruneTombstones(&work, s.nowMs())
	data, err := Encode(work)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.path, data); err != nil {
		return err
	}
	st, _ := stat(s.path)
	s.board, s.stamp, s.loaded, s.loadErr = work, st, true, nil
	return nil
}

func cloneBoard(b Board) Board {
	out := Board{Cursor: b.Cursor, Items: make([]Item, len(b.Items))}
	for i, it := range b.Items {
		it.History = slices.Clone(it.History)
		out.Items[i] = it
	}
	return out
}

func pruneTombstones(b *Board, now int64) {
	cut := now - tombstoneTTL.Milliseconds()
	b.Items = slices.DeleteFunc(b.Items, func(it Item) bool {
		return it.Deleted && (!it.Dirty || it.Updated < cut)
	})
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".kanban-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// find resolves a full uuid, a short id ("K-3f9a2c") or a bare hex prefix of
// at least four digits to the index of one live item.
func find(b *Board, ref string) (int, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	ref = strings.TrimPrefix(ref, "k-")
	ref = strings.TrimPrefix(ref, "k")
	hex := strings.ReplaceAll(ref, "-", "")
	if len(hex) < 4 {
		return -1, ErrNotFound
	}
	match := -1
	for i, it := range b.Items {
		if it.Deleted {
			continue
		}
		if strings.HasPrefix(strings.ReplaceAll(it.ID, "-", ""), hex) {
			if match >= 0 {
				return -1, ErrAmbiguous
			}
			match = i
		}
	}
	if match < 0 {
		return -1, ErrNotFound
	}
	return match, nil
}

// Get returns one item by id or short id.
func (s *Store) Get(ref string) (Item, error) {
	var out Item
	var ferr error
	err := s.read(func(b *Board) {
		i, err := find(b, ref)
		if err != nil {
			ferr = err
			return
		}
		out = b.Items[i]
	})
	if err != nil {
		return Item{}, err
	}
	return out, ferr
}

// Search returns the live items matching q, most recently updated first.
func (s *Store) Search(q Query) ([]Item, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	words := strings.Fields(strings.ToLower(q.Text))
	var out []Item
	err := s.read(func(b *Board) {
		for _, it := range b.Items {
			if it.Deleted {
				continue
			}
			if len(q.Lists) > 0 && !slices.Contains(q.Lists, it.List) {
				continue
			}
			if q.Project != "" && !strings.EqualFold(it.Project, q.Project) && it.ProjectKey != q.Project {
				continue
			}
			if len(words) > 0 {
				hay := strings.ToLower(it.Title + "\n" + it.Body + "\n" + it.Short())
				for _, m := range it.History {
					hay += "\n" + strings.ToLower(m.Note)
				}
				all := true
				for _, w := range words {
					if !strings.Contains(hay, w) {
						all = false
						break
					}
				}
				if !all {
					continue
				}
			}
			it.History = slices.Clone(it.History)
			out = append(out, it)
		}
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, err
}

// Counts returns how many live items each list holds for project ("" = all).
func (s *Store) Counts(project string) (map[List]int, error) {
	out := map[List]int{}
	err := s.read(func(b *Board) {
		for _, it := range b.Items {
			if it.Deleted {
				continue
			}
			if project != "" && !strings.EqualFold(it.Project, project) && it.ProjectKey != project {
				continue
			}
			out[it.List]++
		}
	})
	return out, err
}

func normTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func appendHistory(it *Item, m Move) {
	it.History = append(it.History, m)
	if n := len(it.History); n > MaxHistory {
		it.History = slices.Clone(it.History[n-MaxHistory:])
	}
}

// Add puts a new item on the board. When a live, unfinished item in the same
// project already has the same title, that item is returned with dup true and
// nothing is written.
func (s *Store) Add(in ItemInput, prov Provenance) (Item, bool, error) {
	title := CleanTitle(in.Title)
	if title == "" {
		return Item{}, false, ErrNoTitle
	}
	list := in.List
	if list == "" {
		list = Backlog
	}
	if !list.Valid() {
		return Item{}, false, ErrBadList
	}
	body := CleanBody(in.Body, MaxBodyBytes)
	var out Item
	var dup bool
	err := s.mutate(true, func(b *Board) error {
		live := 0
		want := normTitle(title)
		for _, it := range b.Items {
			if it.Deleted {
				continue
			}
			live++
			if it.List != Done && normTitle(it.Title) == want && sameProject(it, prov) {
				out, dup = it, true
			}
		}
		if dup {
			return errNoWrite
		}
		if live >= MaxItems {
			return ErrFull
		}
		now := s.nowMs()
		id, err := session.NewID()
		if err != nil {
			return err
		}
		out = Item{
			ID: id, Title: title, Body: body, List: list,
			Project: CleanTitle(prov.Project), ProjectKey: prov.ProjectKey, Dir: prov.Dir,
			HostID: prov.HostID, SessionID: prov.SessionID,
			Created: now, Updated: now, Dirty: true,
		}
		appendHistory(&out, Move{ID: session.MustID(), To: list, At: now, SessionID: prov.SessionID})
		b.Items = append(b.Items, out)
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return out, dup, nil
	}
	return out, false, err
}

// errNoWrite aborts a mutation that turned out to have nothing to write.
var errNoWrite = errors.New("no write")

func sameProject(it Item, prov Provenance) bool {
	if it.ProjectKey != "" && prov.ProjectKey != "" {
		return it.ProjectKey == prov.ProjectKey
	}
	return strings.EqualFold(it.Project, prov.Project)
}

// Update edits an item's title or body and/or appends a note.
func (s *Store) Update(ref string, p Patch, sessionID string) (Item, error) {
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		changed := false
		if p.Title != nil {
			t := CleanTitle(*p.Title)
			if t == "" {
				return ErrNoTitle
			}
			if t != it.Title {
				it.Title, changed = t, true
			}
		}
		if p.Body != nil {
			if body := CleanBody(*p.Body, MaxBodyBytes); body != it.Body {
				it.Body, changed = body, true
			}
		}
		now := s.nowMs()
		if note := CleanBody(p.Note, MaxNoteBytes); note != "" {
			appendHistory(it, Move{ID: session.MustID(), From: it.List, To: it.List, At: now, SessionID: sessionID, Note: note})
			changed = true
		}
		if !changed {
			out = *it
			return errNoWrite
		}
		it.Updated, it.Dirty = now, true
		out = *it
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return out, nil
	}
	return out, err
}

// Move moves an item to another list, recording the move and its note.
func (s *Store) Move(ref string, to List, note, sessionID string) (Item, error) {
	if !to.Valid() {
		return Item{}, ErrBadList
	}
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		now := s.nowMs()
		appendHistory(it, Move{ID: session.MustID(), From: it.List, To: to, At: now, SessionID: sessionID, Note: CleanBody(note, MaxNoteBytes)})
		it.List, it.Updated, it.Dirty = to, now, true
		out = *it
		return nil
	})
	return out, err
}

// Delete removes an item. It stays as a tombstone until sync pushes the
// delete.
func (s *Store) Delete(ref string) (Item, error) {
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		it.Deleted, it.Dirty, it.Updated = true, true, s.nowMs()
		out = *it
		return nil
	})
	return out, err
}
