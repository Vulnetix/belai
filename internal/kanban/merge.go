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
				it = cloneItem(it)
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
			// A pulled copy older than what this host already holds is stale:
			// another process on this host pushed a newer change (and marked
			// it clean) while this pull was in flight. The backend's own
			// last-writer-wins keeps updatedAt and version monotonic per item,
			// so a lower value can only be the past. Letting it win silently
			// reverted a worker's claim, and a second worker claimed the item.
			stale := r.ServerVersion < local.ServerVersion || r.Updated < local.Updated
			if localWins || stale {
				local.History = hist
				if r.ServerVersion > local.ServerVersion {
					local.ServerVersion = r.ServerVersion
				}
				continue
			}
			r.History = hist
			r.Dirty = false
			keepAgent(&r, *local)
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

// keepAgent settles the routing and claim fields of a pulled item that won
// last-writer-wins. A backend that does not carry them sent none, and the
// local values stand. Lease renewals are host-local and never pushed, so a
// pulled copy of the same claim carries an older lease: the later one stands,
// or the claim would lapse under a working worker.
func keepAgent(r *Item, local Item) {
	if !r.remoteAgent {
		r.Labels, r.Priority, r.Assignee = local.Labels, local.Priority, local.Assignee
		r.Parent, r.DependsOn, r.Hops = local.Parent, local.DependsOn, local.Hops
		r.ClaimedBy, r.ClaimHost, r.ClaimFrom = local.ClaimedBy, local.ClaimHost, local.ClaimFrom
		r.LeaseUntil, r.Attempts, r.Branch, r.PR = local.LeaseUntil, local.Attempts, local.Branch, local.PR
		return
	}
	if r.ClaimedBy != "" && r.ClaimedBy == local.ClaimedBy && local.LeaseUntil > r.LeaseUntil {
		r.LeaseUntil = local.LeaseUntil
	}
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
	r.Labels = NormLabels(r.Labels)
	r.Priority = ClampPriority(r.Priority)
	if a, err := CleanAssignee(r.Assignee); err == nil {
		r.Assignee = a
	} else {
		r.Assignee = ""
	}
	r.Parent = CleanTitle(r.Parent)
	var deps []string
	for _, d := range r.DependsOn {
		if d = CleanTitle(d); d != "" && len(deps) < MaxDepends {
			deps = append(deps, d)
		}
	}
	r.DependsOn = deps
	r.Hops = max(r.Hops, 0)
	r.Attempts = max(r.Attempts, 0)
	r.ClaimedBy, r.ClaimHost = CleanTitle(r.ClaimedBy), CleanTitle(r.ClaimHost)
	if r.ClaimFrom != "" && !r.ClaimFrom.Valid() {
		r.ClaimFrom = ""
	}
	if r.ClaimedBy == "" {
		r.ClaimHost, r.ClaimFrom, r.LeaseUntil = "", "", 0
	}
	r.Branch, r.PR = CleanTitle(r.Branch), CleanTitle(r.PR)
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
