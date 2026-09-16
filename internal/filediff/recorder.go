package filediff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Defaults for a Recorder. They exist to bound the cost of observing a change,
// which is paid on every mutating command whether or not anything changed.
const (
	DefaultGitTimeout = 1500 * time.Millisecond
	DefaultMaxFiles   = 20
	DefaultMaxBytes   = 1 << 20 // per file
	DefaultMaxDirty   = 200     // pre-existing dirty paths we will snapshot
	maxHeuristicFiles = 32
	heuristicMaxBytes = 512 << 10
)

// Recorder observes what a command changed.
//
// A Recorder is used from one goroutine at a time: mutating tools always run
// on the sequential path because Kind.ReadOnly() is false for them, which is
// what keeps this single-goroutine.
type Recorder struct {
	Root     string // the working directory commands run in
	RepoRoot string // "" when Root is not inside a git repository

	GitTimeout time.Duration
	MaxFiles   int
	MaxBytes   int64
	MaxDirty   int

	// disabled latches on after git proves too slow to consult. One slow
	// repository should cost the session once, not once per command.
	disabled bool
	// gitUnavailable latches when git is not installed.
	gitUnavailable bool
}

// NewRecorder builds a Recorder for a working directory, detecting whether it
// sits inside a git repository.
func NewRecorder(root string) *Recorder {
	r := &Recorder{
		Root:       root,
		GitTimeout: DefaultGitTimeout,
		MaxFiles:   DefaultMaxFiles,
		MaxBytes:   DefaultMaxBytes,
		MaxDirty:   DefaultMaxDirty,
	}
	r.RepoRoot = detectRepoRoot(root)
	return r
}

// Snapshot is the state of the workspace before a command ran.
type Snapshot struct {
	rec *Recorder

	// git path
	before map[string]string // repo-relative path -> status code
	baseln map[string]string // repo-relative path -> content, for paths already dirty

	// heuristic path
	targets []string          // absolute paths
	existed map[string]string // absolute path -> content ("" means absent)

	unavailable string
}

// Before captures the state a command is about to change. A nil Snapshot is
// valid and yields an empty Change.
func (r *Recorder) Before(ctx context.Context, command string) *Snapshot {
	if r == nil || r.disabled {
		return nil
	}
	if r.RepoRoot != "" && !r.gitUnavailable {
		return r.beforeGit(ctx)
	}
	return r.beforeHeuristic(command)
}

// BeforePaths captures the state of the given paths before a mutating tool
// runs. Inside a git repository it delegates to beforeGit; outside one it
// snapshots exactly the given paths, including ones that do not exist yet, so
// a file creation still yields a diff.
func (r *Recorder) BeforePaths(ctx context.Context, paths ...string) *Snapshot {
	if r == nil || r.disabled {
		return nil
	}
	if r.RepoRoot != "" && !r.gitUnavailable {
		return r.beforeGit(ctx)
	}
	return r.beforePathsHeuristic(paths)
}

// beforePathsHeuristic snapshots explicit paths outside a git repository. It
// differs from beforeHeuristic in two ways: the paths are trusted (they came
// from the tool, not a parsed command), and a non-existent path is recorded as
// "" and still added to targets, or a creation would produce no diff at all.
func (r *Recorder) beforePathsHeuristic(paths []string) *Snapshot {
	if len(paths) == 0 {
		return nil
	}
	if len(paths) > maxHeuristicFiles {
		return &Snapshot{rec: r, unavailable: "too many files to watch"}
	}

	s := &Snapshot{rec: r, existed: map[string]string{}}
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(r.Root, p)
		}
		if !r.within(abs) {
			continue
		}
		s.targets = append(s.targets, abs)
		if body, ok := readCapped(abs, heuristicMaxBytes); ok {
			s.existed[abs] = body
		} else {
			s.existed[abs] = "" // may not exist yet (a creation)
		}
	}
	if len(s.targets) == 0 {
		return nil
	}
	return s
}

// After computes what changed. It is safe to call on a nil Snapshot.
func (s *Snapshot) After(ctx context.Context) Change {
	if s == nil {
		return Change{}
	}
	if s.unavailable != "" {
		return Change{Unavailable: s.unavailable}
	}
	if s.before != nil {
		return s.afterGit(ctx)
	}
	return s.afterHeuristic()
}

// ---- git path -------------------------------------------------------------

// beforeGit records the working tree's status, and the content of anything
// already dirty.
//
// A path that is clean now needs no snapshot: git is already holding its
// baseline, and the index copy is unaffected by a later write to the worktree,
// so it can be fetched afterwards instead. On a clean tree this costs one
// subprocess and no file reads.
func (r *Recorder) beforeGit(ctx context.Context) *Snapshot {
	status, err := r.gitStatus(ctx)
	if err != nil {
		return &Snapshot{rec: r, unavailable: err.Error()}
	}

	s := &Snapshot{rec: r, before: status, baseln: map[string]string{}}
	dirty := 0
	for path, code := range status {
		if code == "??" {
			continue // untracked: its baseline is "nothing"
		}
		dirty++
		if dirty > r.MaxDirty {
			return &Snapshot{rec: r, unavailable: "worktree too dirty for diff capture"}
		}
		if body, ok := readCapped(filepath.Join(r.RepoRoot, path), r.MaxBytes); ok {
			s.baseln[path] = body
		}
	}
	return s
}

func (s *Snapshot) afterGit(ctx context.Context) Change {
	r := s.rec
	after, err := r.gitStatus(ctx)
	if err != nil {
		return Change{Unavailable: err.Error()}
	}

	changed := map[string]bool{}
	for path, code := range after {
		if before, ok := s.before[path]; !ok || before != code {
			changed[path] = true
			continue
		}
		// Same status code on both sides still hides a change: a file that was
		// already modified and was modified again stays " M" throughout. Only
		// the content settles it.
		if base, ok := s.baseln[path]; ok {
			if body, ok := readCapped(filepath.Join(r.RepoRoot, path), r.MaxBytes); ok &&
				sha256.Sum256([]byte(body)) != sha256.Sum256([]byte(base)) {
				changed[path] = true
			}
		}
	}
	for path := range s.before {
		if _, ok := after[path]; !ok {
			changed[path] = true // staged, committed, reverted or removed
		}
	}

	if len(changed) == 0 {
		return Change{}
	}
	if len(changed) > r.MaxFiles {
		return Change{Unavailable: plural(len(changed), "file") + " changed (too many to show)"}
	}

	paths := make([]string, 0, len(changed))
	for p := range changed {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var files []FileChange
	for _, path := range paths {
		fc := s.fileChangeGit(ctx, path)
		if fc.Old == fc.New && !fc.Created && !fc.Deleted {
			continue // e.g. `git add`: the index moved, the content did not
		}
		files = append(files, fc)
	}
	if len(files) == 0 {
		return Change{}
	}
	return Change{Files: files}
}

// fileChangeGit resolves one path's before and after.
func (s *Snapshot) fileChangeGit(ctx context.Context, path string) FileChange {
	r := s.rec
	abs := filepath.Join(r.RepoRoot, path)

	fc := FileChange{Path: r.displayPath(abs)}

	// Old side: our own snapshot when the path was already dirty, the index
	// copy when it was clean, and nothing when it was untracked.
	switch code, known := s.before[path]; {
	case !known:
		fc.Old, _ = r.gitShowIndex(ctx, path)
	case code == "??":
		fc.Old = ""
	default:
		if body, ok := s.baseln[path]; ok {
			fc.Old = body
		} else {
			fc.Old, _ = r.gitShowIndex(ctx, path)
		}
	}

	if body, ok := readCapped(abs, r.MaxBytes); ok {
		fc.New = body
	} else if _, err := os.Stat(abs); err != nil {
		fc.Deleted = true
	} else {
		fc.Truncated = true
	}

	if fc.Old == "" && fc.New != "" {
		fc.Created = true
	}
	if isBinary(fc.Old) || isBinary(fc.New) {
		fc.Binary = true
		fc.Old, fc.New = "", ""
	}
	return fc
}

// gitStatus lists every path git considers changed, relative to the repository
// root.
//
// --porcelain=v1 paths are repo-root-relative whatever -C is, which is what
// makes a workdir below the root work. -z removes quoting questions. -uall
// lists untracked files individually while still honouring .gitignore.
// --no-optional-locks keeps this from refreshing the index under a concurrent
// git process.
func (r *Recorder) gitStatus(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.GitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", r.RepoRoot, "--no-optional-locks",
		"status", "--porcelain=v1", "-z", "-uall", "--no-renames")
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			r.disabled = true
			return nil, errSlowGit
		}
		r.gitUnavailable = true
		return nil, errNoGit
	}

	status := map[string]string{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) < 4 {
			continue
		}
		status[string(rec[3:])] = string(rec[:2])
	}
	return status, nil
}

// gitShowIndex returns a path's staged content, which a write to the worktree
// does not disturb — so it is a valid "before" even when fetched afterwards.
func (r *Recorder) gitShowIndex(ctx context.Context, path string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, r.GitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", r.RepoRoot, "--no-optional-locks",
		"show", ":"+path)
	out, err := cmd.Output()
	if err != nil || int64(len(out)) > r.MaxBytes {
		return "", false
	}
	return string(out), true
}

// ---- heuristic path -------------------------------------------------------

func (r *Recorder) beforeHeuristic(command string) *Snapshot {
	targets, confident := ParseTargets(command)
	if !confident {
		return &Snapshot{rec: r, unavailable: unavailableUnknown(r.RepoRoot)}
	}
	if len(targets) == 0 {
		return nil // nothing to watch
	}
	if len(targets) > maxHeuristicFiles {
		return &Snapshot{rec: r, unavailable: "too many files to watch"}
	}

	s := &Snapshot{rec: r, existed: map[string]string{}}
	for _, t := range targets {
		abs := t
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(r.Root, t)
		}
		if !r.within(abs) || !isRegular(abs) {
			continue
		}
		body, _ := readCapped(abs, heuristicMaxBytes)
		s.existed[abs] = body
		s.targets = append(s.targets, abs)
	}
	if len(s.targets) == 0 {
		return nil
	}
	return s
}

func (s *Snapshot) afterHeuristic() Change {
	var files []FileChange
	for _, abs := range s.targets {
		old := s.existed[abs]
		fc := FileChange{Path: s.rec.displayPath(abs), Old: old}

		if body, ok := readCapped(abs, heuristicMaxBytes); ok {
			fc.New = body
		} else if _, err := os.Stat(abs); err != nil {
			fc.Deleted = true
		}
		if fc.Old == fc.New {
			continue
		}
		if fc.Old == "" && fc.New != "" {
			fc.Created = true
		}
		if isBinary(fc.Old) || isBinary(fc.New) {
			fc.Binary = true
			fc.Old, fc.New = "", ""
		}
		files = append(files, fc)
	}
	if len(files) == 0 {
		return Change{}
	}
	if len(files) > s.rec.MaxFiles {
		return Change{Unavailable: plural(len(files), "file") + " changed (too many to show)"}
	}
	return Change{Files: files}
}

// ---- helpers --------------------------------------------------------------

var (
	errSlowGit = &staticErr{"git was too slow; diffs are off for this session"}
	errNoGit   = &staticErr{"git is not available"}
)

type staticErr struct{ s string }

func (e *staticErr) Error() string { return e.s }

func unavailableUnknown(repoRoot string) string {
	if repoRoot == "" {
		return "diff unavailable: not a git repo and the command's targets are unknown"
	}
	return "diff unavailable: the command's targets are unknown"
}

// detectRepoRoot finds the repository root containing dir, if any. It walks
// upward rather than shelling out, so the common non-repo case costs nothing.
func detectRepoRoot(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		// .git is a directory in a normal clone and a file in a worktree or
		// submodule; both mark a root.
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}

// displayPath renders an absolute path relative to the working directory.
func (r *Recorder) displayPath(abs string) string {
	if rel, err := filepath.Rel(r.Root, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
}

// within reports whether a path stays inside the working directory.
func (r *Recorder) within(abs string) bool {
	rel, err := filepath.Rel(r.Root, abs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readCapped(path string, maxBytes int64) (string, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxBytes {
		return "", false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(body), true
}

func isRegular(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// isBinary treats a NUL byte as the marker, which is what git does.
func isBinary(s string) bool { return strings.IndexByte(s, 0) >= 0 }

func plural(n int, word string) string {
	out := itoa(n) + " " + word
	if n != 1 {
		out += "s"
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// expandGlob resolves a pattern to concrete paths, refusing anything that
// matches too much to be an edit.
func expandGlob(p string) ([]string, bool) {
	if !strings.ContainsAny(p, "*?[") {
		return []string{p}, true
	}
	matches, err := filepath.Glob(p)
	if err != nil || len(matches) > maxGlobMatches {
		return nil, false
	}
	return matches, true
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
