// Package projectregistry keeps a lightweight registry of projects that have
// been opened or discovered, indexed by session.WorkdirKey so the history
// screen can list them without walking the filesystem every time.
package projectregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/gitinfo"
	"github.com/vulnetix/signet/internal/session"
)

// CurrentVersion is the on-disk format version.
const CurrentVersion = 1

// Source identifies how an entry was discovered. Higher values win during merge.
type Source string

const (
	SourceSweep   Source = "sweep"
	SourceWorkdir Source = "workdir"
	SourceReview  Source = "review"
	SourceManual  Source = "manual"
)

// precedence returns a comparable rank; manual > review > workdir > sweep.
func (s Source) precedence() int {
	switch s {
	case SourceManual:
		return 4
	case SourceReview:
		return 3
	case SourceWorkdir:
		return 2
	case SourceSweep:
		return 1
	}
	return 0
}

// Entry is one registered project.
type Entry struct {
	Key          string    `json:"key"`
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	HasGit       bool      `json:"has_git"`
	GitRemote    string    `json:"git_remote,omitempty"`
	GitBranch    string    `json:"git_branch,omitempty"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	LastReview   time.Time `json:"last_review,omitempty"`
	ArtifactTime time.Time `json:"artifact_time,omitempty"`
	Source       Source    `json:"source"`
	Missing      bool      `json:"missing"`
	Pinned       bool      `json:"pinned"`
}

// File is the on-disk JSON shape.
type File struct {
	Version   int       `json:"version"`
	LastSweep time.Time `json:"last_sweep,omitempty"`
	Entries   []Entry   `json:"entries"`
}

// Registry is the in-memory view.
type Registry struct {
	file File
	mu   sync.Mutex
}

// registryPath returns the path to projects.json. It is computed each call so
// tests can vary $SIGNET_HOME between subtests.
func registryPath() (string, error) {
	gd, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "projects.json"), nil
}

// lockfilePath returns the advisory-lock path.
func lockfilePath() (string, error) {
	p, err := registryPath()
	if err != nil {
		return "", err
	}
	return p + ".lock", nil
}

// ErrorForwardVersion is returned when the on-disk version is newer than this
// binary understands.
var ErrorForwardVersion = errors.New("projects.json version is newer than this binary")

// Load reads the registry from disk. A missing file returns an empty registry.
func Load() (Registry, error) {
	path, err := registryPath()
	if err != nil {
		return Registry{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Registry{file: File{Version: CurrentVersion}}, nil
		}
		return Registry{}, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return Registry{}, fmt.Errorf("parse projects.json: %w", err)
	}
	if f.Version > CurrentVersion {
		// Read-only when the file is newer; refuse to write later.
		return Registry{file: f}, ErrorForwardVersion
	}
	if f.Version < CurrentVersion {
		f.Version = CurrentVersion
	}
	return Registry{file: f}, nil
}

// LastSweep returns the timestamp of the last full sweep.
func (r *Registry) LastSweep() time.Time { return r.file.LastSweep }

// SetLastSweep updates the last-sweep timestamp.
func (r *Registry) SetLastSweep(t time.Time) { r.file.LastSweep = t }

// All returns entries sorted by LastSeen desc.
func (r *Registry) All() []Entry {
	out := make([]Entry, len(r.file.Entries))
	copy(out, r.file.Entries)
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

// Mutate is the only write path. It re-reads the file under an advisory lock
// before applying fn, so concurrent in-process writers see each other's work.
func Mutate(fn func(*Registry) error) error {
	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()

	reg, err := Load()
	if err != nil && !errors.Is(err, ErrorForwardVersion) {
		return err
	}
	if err == ErrorForwardVersion {
		return ErrorForwardVersion
	}
	if err := fn(&reg); err != nil {
		return err
	}
	return reg.save()
}

// save writes the registry atomically.
func (r *Registry) save() error {
	path, err := registryPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(r.file, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteGlobalFileAtomic(path, data)
}

// acquireLock takes an advisory lockfile, stealing it if it is stale.
func acquireLock() (func(), error) {
	path, err := lockfilePath()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			pid := fmt.Sprintf("%d", os.Getpid())
			_, _ = f.WriteString(pid)
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if fi, stErr := os.Stat(path); stErr == nil && time.Since(fi.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not acquire registry lock")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Observe records or updates an entry for workdir.
func Observe(workdir string, src Source) error {
	return Mutate(func(r *Registry) error {
		r.observe(workdir, src)
		return nil
	})
}

// MarkReviewed updates the LastReview timestamp for workdir.
func MarkReviewed(workdir string, at time.Time) error {
	return Mutate(func(r *Registry) error {
		key := entryKey(workdir)
		for i := range r.file.Entries {
			if r.file.Entries[i].Key == key {
				if at.After(r.file.Entries[i].LastReview) {
					r.file.Entries[i].LastReview = at
				}
				return nil
			}
		}
		return nil
	})
}

// Forget removes an entry by key.
func Forget(key string) error {
	return Mutate(func(r *Registry) error {
		var out []Entry
		for _, e := range r.file.Entries {
			if e.Key != key {
				out = append(out, e)
			}
		}
		r.file.Entries = out
		return nil
	})
}

// Prune removes entries whose LastSeen is older than d and which are not pinned.
func Prune(olderThan time.Duration, now time.Time) (int, error) {
	var pruned int
	err := Mutate(func(r *Registry) error {
		cutoff := now.Add(-olderThan)
		var out []Entry
		for _, e := range r.file.Entries {
			if !e.Pinned && e.LastSeen.Before(cutoff) {
				pruned++
				continue
			}
			out = append(out, e)
		}
		r.file.Entries = out
		return nil
	})
	return pruned, err
}

func (r *Registry) observe(workdir string, src Source) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		abs = workdir
	}
	key := entryKey(abs)
	info, hasGit := gitinfo.Detect(abs)
	now := time.Now()

	for i := range r.file.Entries {
		e := &r.file.Entries[i]
		if e.Key != key {
			continue
		}
		// Merge: higher-precedence source wins; dates take latest; first
		// seen takes earliest; name/pinned preserved once user-set.
		if src.precedence() > e.Source.precedence() {
			e.Source = src
		}
		if e.FirstSeen.IsZero() || now.Before(e.FirstSeen) {
			e.FirstSeen = now
		}
		if now.After(e.LastSeen) {
			e.LastSeen = now
		}
		e.Missing = false
		e.Path = abs
		e.HasGit = hasGit
		e.GitRemote = info.Root
		if info.Branch != "" {
			e.GitBranch = info.Branch
		}
		if e.Name == "" {
			e.Name = filepath.Base(abs)
		}
		return
	}

	// New entry.
	r.file.Entries = append(r.file.Entries, Entry{
		Key:       key,
		Path:      abs,
		Name:      filepath.Base(abs),
		HasGit:    hasGit,
		GitRemote: info.Root,
		GitBranch: info.Branch,
		FirstSeen: now,
		LastSeen:  now,
		Source:    src,
	})
}

func entryKey(workdir string) string {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		abs = workdir
	}
	return session.WorkdirKey(abs)
}
