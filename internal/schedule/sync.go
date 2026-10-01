package schedule

import (
	"context"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// Remote is the backend half of sync; *sessionsync.Client implements it.
type Remote interface {
	ScheduleList(ctx context.Context, hostID string, since int64, limit int) ([]sessionsync.Schedule, int64, bool, error)
	ScheduleBatch(ctx context.Context, hostID string, items []sessionsync.Schedule) ([]sessionsync.ScheduleAck, error)
}

// maxPullPages bounds one pull: 20 pages of 500 is far more schedules than a
// host may hold.
const maxPullPages = 20

// Pull merges every change the backend holds after the store's cursor and
// returns how many records changed.
func Pull(ctx context.Context, st *Store, r Remote, hostID string) (int, error) {
	cursor, err := st.Cursor()
	if err != nil {
		return 0, err
	}
	total := 0
	for range maxPullPages {
		items, next, more, err := r.ScheduleList(ctx, hostID, cursor, 500)
		if err != nil {
			return total, err
		}
		local := make([]Record, 0, len(items))
		for _, w := range items {
			local = append(local, FromWire(w))
		}
		n, err := st.Merge(local, next)
		if err != nil {
			return total, err
		}
		total += n
		if !more || next <= cursor {
			break
		}
		cursor = next
	}
	return total, nil
}

// Push sends every record with an unpushed change, in batches.
func Push(ctx context.Context, st *Store, r Remote, hostID string) error {
	out, err := st.Outbox()
	if err != nil {
		return err
	}
	for len(out) > 0 {
		n := min(len(out), sessionsync.MaxScheduleBatch)
		batch := make([]sessionsync.Schedule, n)
		sent := make(map[string]int, n)
		for i, rec := range out[:n] {
			batch[i] = ToWire(rec)
			sent[rec.ID] = rec.Rev
		}
		acks, err := r.ScheduleBatch(ctx, hostID, batch)
		if err != nil {
			return err
		}
		pushed := make([]Pushed, 0, len(acks))
		for _, a := range acks {
			if rev, ok := sent[a.ID]; ok {
				pushed = append(pushed, Pushed{ID: a.ID, Rev: rev, Version: a.Version})
			}
		}
		if err := st.MarkPushed(pushed); err != nil {
			return err
		}
		out = out[n:]
	}
	return nil
}

// ToWire converts a record to its wire form. A zero run record is omitted.
func ToWire(r Record) sessionsync.Schedule {
	w := sessionsync.Schedule{
		ID: r.ID, Profile: r.Profile, Cron: r.Cron, Dir: r.Dir, Enabled: r.Enabled,
		LastStatus: r.LastStatus, CreatedAt: r.Created, UpdatedAt: r.Updated,
		Version: r.ServerVersion, Deleted: r.Deleted,
	}
	if r.LastRunAt > 0 {
		t := r.LastRunAt
		w.LastRunAt = &t
	}
	if r.NextRunAt > 0 {
		t := r.NextRunAt
		w.NextRunAt = &t
	}
	return w
}

// FromWire converts a wire schedule. The run record is left out on purpose:
// it is the host's, and Merge never takes it from a pull. The result is
// cleaned by Merge.
func FromWire(w sessionsync.Schedule) Record {
	return Record{
		ID: w.ID, Profile: w.Profile, Cron: w.Cron, Dir: w.Dir, Enabled: w.Enabled,
		Created: w.CreatedAt, Updated: w.UpdatedAt, ServerVersion: w.Version, Deleted: w.Deleted,
	}
}
