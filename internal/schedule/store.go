package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// fileVersion is the on-disk format. A file with a newer version is refused
// rather than rewritten.
const fileVersion = 1

// maxRecords bounds the stored schedules, tombstones included.
const maxRecords = 1000

// File is the schedules file: the records and the backend cursor.
type File struct {
	Version int      `json:"version"`
	Cursor  int64    `json:"cursor"`
	Records []Record `json:"records"`
}

// Store is the local copy of a host's schedules, in schedules.json under the
// global state directory. An in-process mutex and an advisory lockfile
// serialise writers, and a write goes through a temporary file, so a crash
// leaves the old file.
type Store struct {
	path string
	mu   sync.Mutex
}

// ErrForwardVersion is returned for a file written by a newer Belai.
var ErrForwardVersion = errors.New("schedule: schedules.json is newer than this binary")

// Open returns the store at the default path, schedules.json under the global
// state directory.
func Open() (*Store, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return nil, err
	}
	return OpenAt(filepath.Join(dir, "schedules.json")), nil
}

// OpenAt returns the store at path. Nothing is created until the first write.
func OpenAt(path string) *Store { return &Store{path: path} }

func (s *Store) load() (File, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return File{Version: fileVersion}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("schedule: parse %s: %w", filepath.Base(s.path), err)
	}
	if f.Version > fileVersion {
		return File{}, ErrForwardVersion
	}
	f.Version = fileVersion
	return f, nil
}

func (s *Store) read(fn func(File)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	fn(f)
	return nil
}

// mutate applies fn to the freshly read file under both locks and writes the
// result when fn reports a change.
func (s *Store) mutate(fn func(*File) (changed bool, err error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	unlock, err := config.AcquireFileLock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	changed, err := fn(&f)
	if err != nil || !changed {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteGlobalFileAtomic(s.path, data)
}

// All returns copies of every record, tombstones included.
func (s *Store) All() ([]Record, error) {
	var out []Record
	err := s.read(func(f File) { out = slices.Clone(f.Records) })
	return out, err
}

// Live returns copies of the records that are not deleted.
func (s *Store) Live() ([]Record, error) {
	var out []Record
	err := s.read(func(f File) {
		for _, r := range f.Records {
			if !r.Deleted {
				out = append(out, r)
			}
		}
	})
	return out, err
}

// Get returns one live record.
func (s *Store) Get(id string) (Record, bool) {
	var out Record
	var ok bool
	_ = s.read(func(f File) {
		for _, r := range f.Records {
			if r.ID == id && !r.Deleted {
				out, ok = r, true
			}
		}
	})
	return out, ok
}

// Cursor returns the highest backend version pulled so far.
func (s *Store) Cursor() (int64, error) {
	var c int64
	err := s.read(func(f File) { c = f.Cursor })
	return c, err
}

// Outbox returns copies of every record with a change not yet pushed.
func (s *Store) Outbox() ([]Record, error) {
	var out []Record
	err := s.read(func(f File) {
		for _, r := range f.Records {
			if r.Dirty {
				out = append(out, r)
			}
		}
	})
	return out, err
}

// Pushed is the backend's acknowledgement of one pushed record.
type Pushed struct {
	ID      string
	Rev     int   // the Rev the push carried
	Version int64 // the version the backend stored
}

// MarkPushed clears Dirty on records whose pushed change is still the latest
// local one and records the backend's version. A record changed again while
// the push was in flight stays dirty, and a pushed tombstone is dropped.
func (s *Store) MarkPushed(acks []Pushed) error {
	if len(acks) == 0 {
		return nil
	}
	return s.mutate(func(f *File) (bool, error) {
		for _, a := range acks {
			for i := range f.Records {
				r := &f.Records[i]
				if r.ID != a.ID {
					continue
				}
				r.ServerVersion = max(r.ServerVersion, a.Version)
				if r.Rev == a.Rev {
					r.Dirty = false
				}
			}
		}
		f.Records = slices.DeleteFunc(f.Records, func(r Record) bool { return r.Deleted && !r.Dirty })
		return true, nil
	})
}

// Merge folds records pulled from the backend into the store and advances the
// cursor, returning how many records it changed. Each definition resolves
// last-writer-wins on Updated, the backend's version breaking a tie, and a
// newer local change that has not been pushed survives and is pushed next. A
// pulled record never replaces the run record. A definition that changes
// clears the next run and marks the record dirty, so the daemon recomputes it
// and reports it. Pulled text came from the website, so it is cleaned first.
func (s *Store) Merge(remote []Record, cursor int64) (int, error) {
	changed := 0
	err := s.mutate(func(f *File) (bool, error) {
		idx := make(map[string]int, len(f.Records))
		for i, r := range f.Records {
			idx[r.ID] = i
		}
		moved := cursor > f.Cursor
		for _, raw := range remote {
			r := raw.clean()
			if !ValidID(r.ID) {
				continue
			}
			i, have := idx[r.ID]
			if !have {
				if r.Deleted || len(f.Records) >= maxRecords {
					continue
				}
				r.NextRunAt, r.LastRunAt, r.LastStatus = 0, 0, ""
				r.Dirty, r.Rev = true, 1
				idx[r.ID] = len(f.Records)
				f.Records = append(f.Records, r)
				changed++
				moved = true
				continue
			}
			local := &f.Records[i]
			local.ServerVersion = max(local.ServerVersion, r.ServerVersion)
			if local.Dirty && local.Updated > r.Updated {
				continue // the newer local change is pushed next
			}
			if r.Updated < local.Updated || (r.Updated == local.Updated && r.ServerVersion < local.ServerVersion) {
				continue
			}
			if r.Deleted {
				if !local.Deleted {
					local.Deleted, local.Updated, local.Dirty = true, r.Updated, false
					changed++
					moved = true
				}
				continue
			}
			if local.Deleted || !sameDefinition(*local, r) {
				local.Profile, local.Cron, local.Dir, local.Enabled = r.Profile, r.Cron, r.Dir, r.Enabled
				local.Deleted = false
				local.Updated = r.Updated
				local.NextRunAt = 0
				local.Dirty, local.Rev = true, local.Rev+1
				changed++
			}
			moved = true
		}
		f.Records = slices.DeleteFunc(f.Records, func(r Record) bool { return r.Deleted && !r.Dirty })
		if cursor > f.Cursor {
			f.Cursor = cursor
		}
		return moved || changed > 0, nil
	})
	return changed, err
}

// Add stores a new local schedule: the id is the caller's, the definition is
// cleaned, and the record is queued for a push.
func (s *Store) Add(r Record, now time.Time) (Record, error) {
	r = r.clean()
	if !ValidID(r.ID) {
		return Record{}, errors.New("schedule: the id is not a UUID")
	}
	r.Created, r.Updated = now.UnixMilli(), now.UnixMilli()
	r.LastRunAt, r.NextRunAt, r.LastStatus, r.Deleted = 0, 0, "", false
	r.Dirty, r.Rev = true, 1
	err := s.mutate(func(f *File) (bool, error) {
		if slices.ContainsFunc(f.Records, func(x Record) bool { return x.ID == r.ID }) {
			return false, errors.New("schedule: that id exists")
		}
		if len(f.Records) >= maxRecords {
			return false, errors.New("schedule: too many schedules")
		}
		f.Records = append(f.Records, r)
		return true, nil
	})
	return r, err
}

// update applies fn to one live record under the lock.
func (s *Store) update(id string, fn func(*Record) bool) error {
	return s.mutate(func(f *File) (bool, error) {
		for i := range f.Records {
			r := &f.Records[i]
			if r.ID != id || r.Deleted {
				continue
			}
			if fn(r) {
				r.Dirty, r.Rev = true, r.Rev+1
				return true, nil
			}
			return false, nil
		}
		return false, nil
	})
}

// SetNext records when a schedule next fires. It is run-record only: the
// definition's Updated does not move, so it cannot lose to a web edit.
func (s *Store) SetNext(id string, next time.Time) error {
	ms := int64(0)
	if !next.IsZero() {
		ms = next.UnixMilli()
	}
	return s.update(id, func(r *Record) bool {
		if r.NextRunAt == ms {
			return false
		}
		r.NextRunAt = ms
		return true
	})
}

// RecordRun writes a schedule's run record: when it fired, what came of it
// and when it fires next.
func (s *Store) RecordRun(id string, at time.Time, status string, next time.Time) error {
	if !ValidStatus(status) {
		status = StatusError
	}
	return s.update(id, func(r *Record) bool {
		r.LastRunAt, r.LastStatus, r.NextRunAt = at.UnixMilli(), status, 0
		if !next.IsZero() {
			r.NextRunAt = next.UnixMilli()
		}
		return true
	})
}

// SetStatus records the outcome of the last attempt without touching when it
// ran or fires next.
func (s *Store) SetStatus(id, status string) error {
	if !ValidStatus(status) {
		status = StatusError
	}
	return s.update(id, func(r *Record) bool {
		if r.LastStatus == status {
			return false
		}
		r.LastStatus = status
		return true
	})
}

// Refuse disables a schedule the host cannot accept and records why. It is a
// change to the definition, so Updated moves and the website sees the
// schedule turn off with its reason.
func (s *Store) Refuse(id, status string, now time.Time) error {
	if !ValidStatus(status) || status == "" {
		status = StatusError
	}
	return s.update(id, func(r *Record) bool {
		r.Enabled, r.LastStatus, r.NextRunAt = false, status, 0
		r.Updated = max(r.Updated+1, now.UnixMilli())
		return true
	})
}

// Rebase clears the next run of every enabled schedule whose time has already
// passed, so a run missed while the daemon was down is skipped instead of
// fired late. It returns how many it cleared.
func (s *Store) Rebase(now time.Time) (int, error) {
	n := 0
	err := s.mutate(func(f *File) (bool, error) {
		for i := range f.Records {
			r := &f.Records[i]
			if r.Deleted || r.NextRunAt == 0 || r.NextRunAt >= now.UnixMilli() {
				continue
			}
			r.NextRunAt = 0
			r.Dirty, r.Rev = true, r.Rev+1
			n++
		}
		return n > 0, nil
	})
	return n, err
}
