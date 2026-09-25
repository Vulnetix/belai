// Package projectregistry keeps a lightweight registry of projects that have
// been opened or discovered, indexed by session.WorkdirKey so the history
// screen can list them without walking the filesystem every time.
package projectregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/gitinfo"
	"github.com/vulnetix/signet/internal/repomap"
	"github.com/vulnetix/signet/internal/session"
)

// CurrentVersion is the on-disk format version.
const CurrentVersion = 2

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
	// RepoMap holds the harness-computed repository map for the project's
	// current HEAD, when one has been computed and stored. Older files read
	// the missing fields as "absent".
	RepoMap      repomap.Map `json:"repo_map,omitempty"`
	RepoMapHead  string      `json:"repo_map_head,omitempty"`
	RepoMapState string      `json:"repo_map_state,omitempty"` // absent|running|ready|failed
	RepoMapOwner string      `json:"repo_map_owner,omitempty"` // session id that claimed it
	RepoMapAt    time.Time   `json:"repo_map_at,omitempty"`
	// WorkspaceDirs are additional directories added to the session with
	// /add-dir and persisted globally for this project.
	WorkspaceDirs []string `json:"workspace_dirs,omitempty"`
	// Trusted records that the user explicitly affirmed this directory on
	// first launch. Missing entries (or trusted:false) prompt again, and a
	// repo cannot ship its own trust: this lives in the global registry only.
	Trusted   bool      `json:"trusted,omitempty"`
	TrustedAt time.Time `json:"trusted_at,omitempty"`
	// AcceptedProjectDirs / DeclinedProjectDirs record project-proposed
	// workspace_dirs the user has already ruled on, so a folder that grows its
	// list later prompts again and a declined directory does not nag.
	AcceptedProjectDirs []string `json:"accepted_project_dirs,omitempty"`
	DeclinedProjectDirs []string `json:"declined_project_dirs,omitempty"`
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

// mutateMu serialises in-process Mutate calls, so fifty concurrent Observe
// goroutines cannot lose updates through the file-lock's read-modify-write
// window. The advisory lockfile remains the cross-process serialisation point.
var mutateMu sync.Mutex

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
// An in-process mutex serialises first, then the advisory lockfile serialises
// across processes.
func Mutate(fn func(*Registry) error) error {
	mutateMu.Lock()
	defer mutateMu.Unlock()

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

// acquireLock takes the registry's advisory lockfile.
func acquireLock() (func(), error) {
	path, err := lockfilePath()
	if err != nil {
		return nil, err
	}
	return config.AcquireFileLock(path)
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

// RepoMap states.
const (
	RepoMapAbsent  = "absent"
	RepoMapRunning = "running"
	RepoMapReady   = "ready"
	RepoMapFailed  = "failed"
)

// RepoMapStaleAfter is how old a running claim must be before another process
// may reclaim it, so a killed process cannot wedge the repo permanently.
const RepoMapStaleAfter = 5 * time.Minute

// ClaimRepoMap atomically claims the repo-map scan for a project's HEAD. It
// returns true when this caller won the claim and should run the scan.
func ClaimRepoMap(workdir, head, owner string) (bool, error) {
	claimed := false
	err := Mutate(func(r *Registry) error {
		r.observe(workdir, SourceWorkdir)
		e := r.entryByKey(entryKey(workdir))
		if e == nil {
			return nil
		}
		if e.RepoMapState == RepoMapReady && e.RepoMapHead == head {
			return nil
		}
		if e.RepoMapState == RepoMapRunning && time.Since(e.RepoMapAt) < RepoMapStaleAfter {
			return nil
		}
		e.RepoMapState = RepoMapRunning
		e.RepoMapHead = head
		e.RepoMapOwner = owner
		e.RepoMapAt = time.Now()
		claimed = true
		return nil
	})
	return claimed, err
}

// AddWorkspaceDir persists dir as an additional workspace directory for
// workdir. It resolves symlinks, requires a directory, and rejects roots
// that overlap an existing workspace root for the same project so one path
// is never resolvable two ways.
func AddWorkspaceDir(workdir, dir string) error {
	return Mutate(func(r *Registry) error {
		return r.addWorkspaceDir(workdir, dir)
	})
}

func (r *Registry) addWorkspaceDir(workdir, dir string) error {
	r.observe(workdir, SourceWorkdir)
	key := entryKey(workdir)
	e := r.entryByKey(key)
	if e == nil {
		return fmt.Errorf("no registry entry for %s", workdir)
	}

	abs, err := normalizeDir(dir)
	if err != nil {
		return err
	}

	for _, existing := range e.WorkspaceDirs {
		if abs == existing || strings.HasPrefix(abs, existing+string(filepath.Separator)) || strings.HasPrefix(existing, abs+string(filepath.Separator)) {
			return fmt.Errorf("workspace directory overlaps an existing root")
		}
	}
	e.WorkspaceDirs = append(e.WorkspaceDirs, abs)
	return nil
}

// normalizeDir absolutises dir, resolves symlinks and requires a directory.
// It is the one rule for turning a user-supplied path into a persisted root.
func normalizeDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	return abs, nil
}

// Trust records that the user affirms workdir and accepts the given proposed
// workspace directories. It registers a non-git directory too, and routes each
// accepted dir through the same Abs → EvalSymlinks → IsDir → overlap checks as
// /add-dir, so a bad dir fails the whole trust without marking it.
func Trust(workdir string, accept []string) error {
	return Mutate(func(r *Registry) error {
		r.observe(workdir, SourceWorkdir)
		for _, d := range accept {
			if err := r.addWorkspaceDir(workdir, d); err != nil {
				return err
			}
		}
		e := r.entryByKey(entryKey(workdir))
		if e == nil {
			return fmt.Errorf("no registry entry for %s", workdir)
		}
		e.Trusted = true
		e.TrustedAt = time.Now()
		for _, d := range accept {
			abs, err := normalizeDir(d)
			if err != nil {
				return err
			}
			e.AcceptedProjectDirs = appendUnique(e.AcceptedProjectDirs, abs)
		}
		return nil
	})
}

// Decline records that the user refused the given proposed workspace
// directories for workdir, so they are not offered again.
func Decline(workdir string, dirs []string) error {
	return Mutate(func(r *Registry) error {
		r.observe(workdir, SourceWorkdir)
		e := r.entryByKey(entryKey(workdir))
		if e == nil {
			return nil
		}
		for _, d := range dirs {
			abs, err := filepath.Abs(d)
			if err != nil {
				abs = d
			}
			e.DeclinedProjectDirs = appendUnique(e.DeclinedProjectDirs, abs)
		}
		return nil
	})
}

// TrustOf reports whether workdir is trusted and which project-proposed
// directories have been accepted or declined, read-only.
func TrustOf(workdir string) (trusted bool, accepted, declined []string, err error) {
	reg, err := Load()
	if err != nil {
		return false, nil, nil, err
	}
	if e := reg.entryByKey(entryKey(workdir)); e != nil {
		return e.Trusted, e.AcceptedProjectDirs, e.DeclinedProjectDirs, nil
	}
	return false, nil, nil, nil
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

// WorkspaceDirs returns the persisted additional workspace directories for
// workdir, in insertion order.
func WorkspaceDirs(workdir string) []string {
	reg, err := Load()
	if err != nil {
		return nil
	}
	if e := reg.entryByKey(entryKey(workdir)); e != nil {
		out := make([]string, len(e.WorkspaceDirs))
		copy(out, e.WorkspaceDirs)
		return out
	}
	return nil
}

// SetWorkspaceDirs replaces the stored workspace directories for workdir.
// It is used after a project-layer allowlist filters out blocked entries.
func SetWorkspaceDirs(workdir string, dirs []string) error {
	return Mutate(func(r *Registry) error {
		r.observe(workdir, SourceWorkdir)
		e := r.entryByKey(entryKey(workdir))
		if e == nil {
			return nil
		}
		copy := make([]string, len(dirs))
		for i, d := range dirs {
			copy[i] = d
		}
		e.WorkspaceDirs = copy
		return nil
	})
}

// ResolveWorkspaceDirs returns the workspace directories that should be
// attached to a session for workdir. It trusts the registry's persisted
// list, but when allowed is non-empty it filters the list to members of
// allowed. Blocked entries are removed from the registry.
func ResolveWorkspaceDirs(workdir string, allowed []string) ([]string, error) {
	reg := WorkspaceDirs(workdir)
	if len(allowed) == 0 {
		return reg, nil
	}

	allowedSet := make(map[string]bool, len(allowed))
	for _, d := range allowed {
		abs, err := filepath.Abs(d)
		if err != nil {
			abs = d
		}
		abs, err = filepath.EvalSymlinks(abs)
		if err != nil {
			abs = d
		}
		allowedSet[abs] = true
	}

	var filtered []string
	var blocked []string
	for _, d := range reg {
		if allowedSet[d] {
			filtered = append(filtered, d)
		} else {
			blocked = append(blocked, d)
		}
	}
	if len(blocked) > 0 {
		if err := SetWorkspaceDirs(workdir, filtered); err != nil {
			return nil, err
		}
	}
	return filtered, nil
}

// StoreRepoMap stores a scanned map for a project's HEAD.
func StoreRepoMap(workdir string, m repomap.Map) error {
	return Mutate(func(r *Registry) error {
		r.observe(workdir, SourceWorkdir)
		e := r.entryByKey(entryKey(workdir))
		if e == nil {
			return nil
		}
		e.RepoMap = m
		e.RepoMapHead = m.Head
		e.RepoMapState = RepoMapReady
		e.RepoMapAt = time.Now()
		return nil
	})
}

// MarkRepoMapFailed records a failed scan so a later caller may retry.
func MarkRepoMapFailed(workdir string) error {
	return Mutate(func(r *Registry) error {
		r.observe(workdir, SourceWorkdir)
		e := r.entryByKey(entryKey(workdir))
		if e == nil {
			return nil
		}
		e.RepoMapState = RepoMapFailed
		e.RepoMapAt = time.Now()
		return nil
	})
}

// WaitRepoMap blocks until a ready repo map exists for the project's current
// HEAD, or the timeout elapses. It returns ok=false on timeout — the map is an
// accelerant, never a gate. A zero timeout performs a single non-blocking read.
func WaitRepoMap(ctx context.Context, workdir, head string, timeout time.Duration) (repomap.Map, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	for {
		if reg, err := Load(); err == nil {
			if e := reg.entryByKey(entryKey(workdir)); e != nil && e.RepoMapState == RepoMapReady && e.RepoMapHead == head {
				return e.RepoMap, true
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return repomap.Map{}, false
		}
		select {
		case <-ctx.Done():
			return repomap.Map{}, false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// entryByKey returns the registry entry for a key, or nil.
func (r *Registry) entryByKey(key string) *Entry {
	for i := range r.file.Entries {
		if r.file.Entries[i].Key == key {
			return &r.file.Entries[i]
		}
	}
	return nil
}
