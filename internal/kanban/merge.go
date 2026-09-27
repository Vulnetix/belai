package kanban

import (
	"slices"
	"sort"
)

// Outbox returns copies of every item with a local change not yet pushed,
// tombstones included.
func (s *Store) Outbox() ([]Item, error) {
	var out []Item
	err := s.read(func(b *Board) {
		for _, it := range b.Items {
			if it.Dirty {
				it.History = slices.Clone(it.History)
				out = append(out, it)
			}
		}
	})
	return out, err
}

// Cursor returns the highest backend version pulled so far.
func (s *Store) Cursor() (int64, error) {
	var c int64
	err := s.read(func(b *Board) { c = b.Cursor })
	return c, err
}

// Pushed is the backend's acknowledgement of one pushed item.
type Pushed struct {
	ID      string
	Updated int64 // the Updated the push carried
	Version int64 // the version the backend stored
}

// MarkPushed clears Dirty on items whose pushed change is still the latest
// local one, records their server version, and drops pushed tombstones. An
// item edited again while the push was in flight stays dirty.
func (s *Store) MarkPushed(acks []Pushed) error {
	if len(acks) == 0 {
		return nil
	}
	return s.mutate(false, func(b *Board) error {
		for _, a := range acks {
			for i := range b.Items {
				it := &b.Items[i]
				if it.ID != a.ID {
					continue
				}
				if a.Version > it.ServerVersion {
					it.ServerVersion = a.Version
				}
				if it.Updated == a.Updated {
					it.Dirty = false
				}
			}
		}
		b.Items = slices.DeleteFunc(b.Items, func(it Item) bool { return it.Deleted && !it.Dirty })
		return nil
	})
}

// Merge folds items pulled from the backend into the board and advances the
// cursor. Each item resolves last-writer-wins on Updated, the server version
// breaking a tie; a newer local change that has not been pushed survives and
// is pushed next. Histories are unioned by move id. Pulled text is cleaned
// exactly like local writes: it came from a web page or another host.
func (s *Store) Merge(remote []Item, cursor int64) (int, error) {
	changed := 0
	err := s.mutate(false, func(b *Board) error {
		idx := make(map[string]int, len(b.Items))
		for i, it := range b.Items {
			idx[it.ID] = i
		}
		for _, r := range remote {
			if r.ID == "" {
				continue
			}
			r = cleanRemote(r)
			i, ok := idx[r.ID]
			if !ok {
				if r.Deleted {
					continue
				}
				r.Dirty = false
				b.Items = append(b.Items, r)
				idx[r.ID] = len(b.Items) - 1
				changed++
				continue
			}
			local := &b.Items[i]
			hist := unionHistory(local.History, r.History)
			localWins := local.Dirty && (local.Updated > r.Updated ||
				(local.Updated == r.Updated && local.ServerVersion >= r.ServerVersion))
			if localWins {
				local.History = hist
				if r.ServerVersion > local.ServerVersion {
					local.ServerVersion = r.ServerVersion
				}
				continue
			}
			r.History = hist
			r.Dirty = false
			*local = r
			changed++
		}
		if cursor > b.Cursor {
			b.Cursor = cursor
		}
		return nil
	})
	return changed, err
}

// cleanRemote applies the local write rules to a pulled item.
func cleanRemote(r Item) Item {
	r.Title = CleanTitle(r.Title)
	if r.Title == "" {
		r.Title = "(untitled)"
	}
	r.Body = CleanBody(r.Body, MaxBodyBytes)
	r.Project = CleanTitle(r.Project)
	r.ProjectKey = CleanTitle(r.ProjectKey)
	r.Dir = CleanTitle(r.Dir)
	r.HostID = CleanTitle(r.HostID)
	r.SessionID = CleanTitle(r.SessionID)
	if !r.List.Valid() {
		r.List = Review
	}
	hist := make([]Move, 0, len(r.History))
	for _, m := range r.History {
		if m.ID == "" || !m.To.Valid() {
			continue
		}
		if m.From != "" && !m.From.Valid() {
			m.From = ""
		}
		m.Note = CleanBody(m.Note, MaxNoteBytes)
		m.SessionID = CleanTitle(m.SessionID)
		hist = append(hist, m)
	}
	r.History = hist
	return r
}

func unionHistory(a, b []Move) []Move {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]Move, 0, len(a)+len(b))
	for _, m := range slices.Concat(a, b) {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	if n := len(out); n > MaxHistory {
		out = out[n-MaxHistory:]
	}
	return out
}
