// Package fleet runs Belai worker agents that take their work from the
// global kanban board (docs/fleet.md). A worker is its own `belai agent run`
// process: it claims an item under the board's lock, works it as a goal in an
// isolated git worktree, and releases it to the next list, for the next agent
// or a human. The registry lets the CLI and the TUI list, watch and stop the
// workers on this machine.
//
// The harness makes every claim and every release. Item text reaches the
// model only as a classified attachment; the notes the harness writes back
// are its own facts, never model text.
package fleet

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/proc"
)

// State is a worker's lifecycle state.
type State string

// Worker states.
const (
	StateStarting State = "starting"
	StateIdle     State = "idle"
	StateWorking  State = "working"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// Live reports whether the state is one a running process holds.
func (s State) Live() bool {
	return s == StateStarting || s == StateIdle || s == StateWorking || s == StateStopping
}

// Record is one worker's entry in the registry: harness facts only.
type Record struct {
	ID          string `json:"id"`
	Profile     string `json:"profile"`
	ProfileHash string `json:"profile_hash,omitempty"`
	Crew        string `json:"crew,omitempty"`
	PID         int    `json:"pid"`
	Repo        string `json:"repo"`
	Project     string `json:"project,omitempty"`
	State       State  `json:"state"`
	// Item is the short id of the item held; Branch its branch.
	Item   string `json:"item,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Session is the transcript session of the current or last item.
	Session  string `json:"session,omitempty"`
	Done     int    `json:"done"`
	Failed   int    `json:"failed"`
	Tokens   int    `json:"tokens,omitempty"`
	Started  int64  `json:"started"`
	Beat     int64  `json:"heartbeat"`
	Stopped  int64  `json:"stopped,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Log      string `json:"log,omitempty"`
	Detached bool   `json:"detached,omitempty"`
}

// Registry is the directory of worker records.
type Registry struct {
	dir   string
	store *kanban.Store // releases a dead worker's claims; may be nil
	now   func() time.Time
}

// OpenRegistry opens the registry under the global state directory.
func OpenRegistry(store *kanban.Store) (*Registry, error) {
	dir, err := config.AgentsDir()
	if err != nil {
		return nil, err
	}
	return NewRegistry(filepath.Join(dir, "run"), store), nil
}

// NewRegistry opens a registry at dir.
func NewRegistry(dir string, store *kanban.Store) *Registry {
	return &Registry{dir: dir, store: store, now: time.Now}
}

// Dir is the registry directory.
func (r *Registry) Dir() string { return r.dir }

// LogDir is the directory worker logs go to.
func (r *Registry) LogDir() string { return filepath.Join(filepath.Dir(r.dir), "logs") }

var idUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// NewID returns a fresh worker id: the profile name made safe, and a random
// suffix ("belai-builder-3f9a2c").
func NewID(profile string) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	base := strings.Trim(idUnsafe.ReplaceAllString(strings.ToLower(profile), "-"), "-")
	if base == "" {
		base = "worker"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	return base + "-" + hex.EncodeToString(b[:])
}

var idShape = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// ValidID reports whether id has a worker id's shape, so an id from the
// command line can never name a path outside the registry.
func ValidID(id string) bool { return idShape.MatchString(id) }

func (r *Registry) path(id string) string { return filepath.Join(r.dir, id+".json") }

// Save writes a record atomically.
func (r *Registry) Save(rec Record) error {
	if !ValidID(rec.ID) {
		return fmt.Errorf("fleet: invalid worker id %q", rec.ID)
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(r.dir, ".rec-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, r.path(rec.ID))
}

// Get reads one record.
func (r *Registry) Get(id string) (Record, error) {
	if !ValidID(id) {
		return Record{}, fmt.Errorf("fleet: invalid worker id %q", id)
	}
	data, err := os.ReadFile(r.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, fmt.Errorf("no worker %q", id)
	}
	if err != nil {
		return Record{}, err
	}
	var rec Record
	return rec, json.Unmarshal(data, &rec)
}

// Remove deletes a record.
func (r *Registry) Remove(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("fleet: invalid worker id %q", id)
	}
	err := os.Remove(r.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// List returns every record, newest first. A record that claims to be live
// but whose process is gone is marked failed, and its claims are released so
// another worker can take the items.
func (r *Registry) List() ([]Record, error) {
	entries, err := os.ReadDir(r.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		rec, err := r.Get(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		if rec.State.Live() && !proc.Alive(rec.PID) {
			rec = r.markDead(rec)
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started > out[j].Started })
	return out, nil
}

func (r *Registry) markDead(rec Record) Record {
	rec.State, rec.Reason, rec.Stopped = StateFailed, "the worker process exited without stopping", r.now().UnixMilli()
	if r.store != nil {
		_, _ = r.store.UnclaimWorker(rec.ID, "released: agent "+rec.Profile+" stopped without releasing it")
	}
	rec.Item = ""
	_ = r.Save(rec)
	return rec
}

// Live returns the records of running workers.
func (r *Registry) Live() ([]Record, error) {
	all, err := r.List()
	var out []Record
	for _, rec := range all {
		if rec.State.Live() {
			out = append(out, rec)
		}
	}
	return out, err
}

// ErrFull is returned when the machine already runs agents.max_workers.
var ErrFull = errors.New("fleet: the maximum number of workers is already running (agents.max_workers)")

// Reserve registers a new worker if a slot is free under max, holding the
// registry lock so two starts cannot both take the last slot.
func (r *Registry) Reserve(rec Record, max int) error {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return err
	}
	release, err := config.AcquireFileLock(filepath.Join(r.dir, ".lock"))
	if err != nil {
		return err
	}
	defer release()
	if existing, err := r.Get(rec.ID); err == nil && existing.State.Live() && proc.Alive(existing.PID) && existing.PID != rec.PID {
		return fmt.Errorf("fleet: worker %s is already running", rec.ID)
	}
	live, err := r.Live()
	if err != nil {
		return err
	}
	n := 0
	for _, l := range live {
		if l.ID != rec.ID {
			n++
		}
	}
	if n >= max {
		return ErrFull
	}
	return r.Save(rec)
}

// Resolve maps a worker id, an id prefix or a profile name to the live
// records it names.
func (r *Registry) Resolve(ref string) ([]Record, error) {
	all, err := r.List()
	if err != nil {
		return nil, err
	}
	var exact, byProfile, byPrefix []Record
	for _, rec := range all {
		switch {
		case rec.ID == ref:
			exact = append(exact, rec)
		case rec.Profile == ref && rec.State.Live():
			byProfile = append(byProfile, rec)
		case strings.HasPrefix(rec.ID, ref) && rec.State.Live():
			byPrefix = append(byPrefix, rec)
		}
	}
	switch {
	case len(exact) > 0:
		return exact, nil
	case len(byProfile) > 0:
		return byProfile, nil
	case len(byPrefix) > 0:
		return byPrefix, nil
	}
	return nil, fmt.Errorf("no worker matches %q", ref)
}

// Stop asks a worker to stop and waits up to grace for it to exit, then
// kills it. Its claims are released either way.
func (r *Registry) Stop(rec Record, grace time.Duration) error {
	if rec.State.Live() && proc.Alive(rec.PID) {
		rec.State = StateStopping
		_ = r.Save(rec)
		_ = proc.Terminate(rec.PID)
		deadline := r.now().Add(grace)
		for proc.Alive(rec.PID) && r.now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if proc.Alive(rec.PID) {
			_ = proc.Kill(rec.PID)
		}
	}
	// A clean stop wrote its own record; reread so a stopped record is not
	// overwritten with a stale one.
	if cur, err := r.Get(rec.ID); err == nil {
		rec = cur
	}
	if rec.State.Live() {
		rec.State, rec.Reason, rec.Stopped = StateStopped, "stopped", r.now().UnixMilli()
		rec.Item = ""
		if err := r.Save(rec); err != nil {
			return err
		}
	}
	if r.store != nil {
		_, _ = r.store.UnclaimWorker(rec.ID, "released: agent "+rec.Profile+" was stopped")
	}
	return nil
}

// Prune removes records of workers that ended more than age ago.
func (r *Registry) Prune(age time.Duration) {
	all, _ := r.List()
	cut := r.now().Add(-age).UnixMilli()
	for _, rec := range all {
		if !rec.State.Live() && rec.Stopped > 0 && rec.Stopped < cut {
			_ = r.Remove(rec.ID)
		}
	}
}

// SpawnOptions starts one detached worker.
type SpawnOptions struct {
	// Exe is the belai binary; Repo the trusted repository it runs in.
	Exe, Repo string
	Profile   string
	Crew      string
	// Provider and Model override the profile's model.
	Provider, Model string
	// Stay keeps the worker waiting for work instead of exiting once the
	// board has nothing left for it.
	Stay bool
}

// Spawn starts a detached `belai agent run` worker and returns its id. The
// worker registers itself; WaitStarted reports whether it did.
func (r *Registry) Spawn(o SpawnOptions) (string, error) {
	id := NewID(o.Profile)
	args := []string{"agent", "run", "-id", id, "-detached"}
	if o.Crew != "" {
		args = append(args, "-crew", o.Crew)
	}
	if o.Provider != "" {
		args = append(args, "-provider", o.Provider)
	}
	if o.Model != "" {
		args = append(args, "-model", o.Model)
	}
	if o.Stay {
		args = append(args, "-stay")
	}
	args = append(args, o.Profile)
	if err := os.MkdirAll(r.LogDir(), 0o700); err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(filepath.Join(r.LogDir(), id+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer logFile.Close()
	cmd := exec.Command(o.Exe, args...)
	cmd.Dir = o.Repo
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := proc.Detach(cmd); err != nil {
		return "", fmt.Errorf("start %s: %w", o.Profile, err)
	}
	return id, nil
}

// WaitStarted waits up to d for each worker to leave the starting state and
// returns the records it found (a missing id has no record yet).
func (r *Registry) WaitStarted(ids []string, d time.Duration) map[string]Record {
	deadline := r.now().Add(d)
	out := map[string]Record{}
	for r.now().Before(deadline) {
		for _, id := range ids {
			if rec, err := r.Get(id); err == nil && rec.State != StateStarting {
				out[id] = rec
			}
		}
		if len(out) == len(ids) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	return out
}
