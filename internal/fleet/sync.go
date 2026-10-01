package fleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Workspace sync (docs/fleet.md#files-placed-in-a-worktree). A worker works in a
// worktree of its own, so the harness puts into it what its profile names:
//
//   - the documents in the profile's knowledge.paths, copied read-only, so the
//     agent can open with the ordinary file tools what the index also finds by
//     meaning (a relative path keeps its place; one outside the project goes
//     under .vulnetix/knowledge/<label>/);
//   - the files and directories in workspace.sync, which a crew shares: copied
//     in before each turn and, with write access, merged back into the
//     repository after it, under a lock.
//
// The worker is never given the repository path. What the harness guarantees,
// whoever wrote the profile: Git's files, Belai's state and credentials and
// the scanner evidence are never synced (agentprofile.ProtectedRead and
// ProtectedWrite); no symlink on either side of a copy; regular text files of
// bounded size and number; the text sanitised before it reaches the repository;
// a merge that never drops a teammate's lines and never overwrites a file Git
// tracks outside .vulnetix; and nothing placed is committed or published.

// Sync bounds.
const (
	maxSyncFileBytes  = 256 << 10
	maxSyncTotalBytes = 1 << 20
	maxSyncFiles      = 64
)

// SyncState is what SyncIn copied, kept for the merge back.
type SyncState struct {
	ws      *Workspace
	repo    string
	entries []syncEntry
}

type syncEntry struct {
	spec agentprofile.SyncSpec
	rel  string
	// base is each file's content as it was copied in; absent means the file
	// did not exist then.
	base map[string][]byte
}

// SyncIn places the profile's reference documents (docs, from
// knowledge.EnumerateProfile) and the listed sync entries in the worktree and
// records what it copied. A missing sync source is not an error: the file is
// simply not there yet, and a write-access worker may create it. Sync paths the
// worktree already holds are replaced with the repository's current copy; a
// reference document never replaces a file the harness did not place.
func (w *Workspace) SyncIn(repo string, specs []agentprofile.SyncSpec, docs []knowledge.ProfileFile) (*SyncState, error) {
	if !w.Worktree || w.Dir == "" || repo == "" {
		return nil, errors.New("sync needs a worktree and its repository")
	}
	st := &SyncState{ws: w, repo: repo}
	var synced []string
	files, total := 0, 0
	for _, spec := range specs {
		rel := spec.Clean()
		if agentprofile.ProtectedRead(rel) || (spec.Writes() && agentprofile.ProtectedWrite(rel)) {
			return nil, fmt.Errorf("%q is never synced", spec.Path)
		}
		e := syncEntry{spec: spec, rel: rel, base: map[string][]byte{}}
		synced = append(synced, rel)
		srcs, err := listSyncSources(repo, rel)
		if err != nil {
			return nil, err
		}
		for _, f := range srcs {
			data, err := readSyncFile(filepath.Join(repo, filepath.FromSlash(f)))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			files++
			total += len(data)
			if files > maxSyncFiles || total > maxSyncTotalBytes {
				return nil, fmt.Errorf("%s holds more than %d files or %d bytes", rel, maxSyncFiles, maxSyncTotalBytes)
			}
			if err := ensureDirs(w.Dir, path.Dir(f)); err != nil {
				return nil, err
			}
			if err := writeSyncFile(filepath.Join(w.Dir, filepath.FromSlash(f)), data); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			e.base[f] = data
		}
		st.entries = append(st.entries, e)
	}
	w.synced = synced
	w.placeDocs(docs)
	return st, nil
}

// placeDocs copies reference documents into the worktree (knowledge.CopyDocs).
// Files an explicit sync entry covers are left to that entry.
func (w *Workspace) placeDocs(docs []knowledge.ProfileFile) {
	if w.placed == nil {
		w.placed = map[string]bool{}
	}
	knowledge.CopyDocs(w.Dir, docs, w.placed, func(dest string) bool {
		return agentprofile.ProtectedRead(dest) || (w.isSynced(dest) && !w.placed[dest])
	}, false)
}

// Out merges every write-access file the worker left in the worktree back into
// the repository and returns the paths it changed. A file the worker did not
// change is left alone. Each merge runs under a lock, and a worker's deletions
// apply only when nobody else changed the file meanwhile: when somebody did,
// the lines the worker added are appended to the current file instead, so a
// teammate's lines are never lost. Content is sanitised first. A file that
// would grow past the size limit is not merged.
func (s *SyncState) Out(ctx context.Context) ([]string, error) {
	var changed []string
	var errs []error
	for _, e := range s.entries {
		if !e.spec.Writes() {
			continue
		}
		mine, err := listWorktreeFiles(s.ws.Dir, e.rel)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, f := range mine {
			if err := ctx.Err(); err != nil {
				return changed, err
			}
			data, err := readSyncFile(filepath.Join(s.ws.Dir, filepath.FromSlash(f)))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", f, err))
				continue
			}
			data = []byte(sanitize.Text(string(data)))
			base, hadBase := e.base[f]
			if hadBase && bytes.Equal(data, base) {
				continue
			}
			did, err := s.merge(ctx, f, base, data)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", f, err))
				continue
			}
			if did {
				changed = append(changed, f)
			}
		}
	}
	sort.Strings(changed)
	return changed, errors.Join(errs...)
}

func (s *SyncState) merge(ctx context.Context, rel string, base, mine []byte) (bool, error) {
	if agentprofile.ProtectedWrite(rel) {
		return false, errors.New("never written back")
	}
	if !strings.HasPrefix(rel, ".vulnetix/") {
		// A file Git tracks is changed on a branch, not under the user's
		// checkout: the write-back is for notes and scratch files. If git
		// cannot say, the file is not written.
		if tracked, err := s.ws.trackedInRepo(ctx, s.repo, rel); err != nil || tracked {
			if err != nil {
				return false, fmt.Errorf("cannot tell whether git tracks it: %w", err)
			}
			return false, errors.New("git tracks it, so a change goes on a branch, not into the checkout")
		}
	}
	if err := ensureDirs(s.repo, path.Dir(rel)); err != nil {
		return false, err
	}
	unlock, err := lockSyncFile(s.repo, rel)
	if err != nil {
		return false, err
	}
	defer unlock()
	dst := filepath.Join(s.repo, filepath.FromSlash(rel))
	var cur []byte
	if _, err := os.Lstat(dst); err == nil {
		if cur, err = readSyncFile(dst); err != nil {
			return false, err
		}
	}
	merged := mergeLines(base, cur, mine)
	if bytes.Equal(merged, cur) {
		return false, nil
	}
	if len(merged) > maxSyncFileBytes {
		return false, fmt.Errorf("the merged file would be %d bytes, over the %d limit; keep it shorter", len(merged), maxSyncFileBytes)
	}
	return true, writeSyncFile(dst, merged)
}

// mergeLines is the three-way merge of base (as the worker received it), cur
// (the repository's file now) and mine (the worker's file). When nobody else
// changed the file it is mine. Otherwise it is cur with the lines mine added
// appended, skipping any a teammate already wrote; mine's deletions and edits
// of existing lines are dropped, because applying them could erase a
// teammate's work.
func mergeLines(base, cur, mine []byte) []byte {
	if bytes.Equal(cur, base) {
		return mine
	}
	baseCount := lineCounts(base)
	curCount := lineCounts(cur)
	var added []string
	for _, l := range splitLines(mine) {
		if baseCount[l] > 0 {
			baseCount[l]--
			continue
		}
		if curCount[l] > 0 {
			curCount[l]--
			continue
		}
		added = append(added, l)
	}
	if len(added) == 0 {
		return cur
	}
	out := bytes.TrimRight(cur, "\n")
	if len(out) > 0 {
		out = append(out, '\n')
	}
	out = append(out, []byte(strings.Join(added, "\n"))...)
	return append(out, '\n')
}

func splitLines(b []byte) []string {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func lineCounts(b []byte) map[string]int {
	m := map[string]int{}
	for _, l := range splitLines(b) {
		m[l]++
	}
	return m
}

// trackedInRepo reports whether the repository's index holds rel.
func (w *Workspace) trackedInRepo(ctx context.Context, repo, rel string) (bool, error) {
	if w.repoRun == nil {
		return false, errors.New("no repository runner")
	}
	out, err := git(ctx, w.repoRun, repo, "ls-files", "--", rel)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// CommittedSynced lists the synced paths the branch has committed since Base.
// The harness never commits them; a model could, with its own git.
func (w *Workspace) CommittedSynced(ctx context.Context) []string {
	if !w.Worktree || w.Base == "" || len(w.synced) == 0 {
		return nil
	}
	out, err := git(ctx, w.run, w.Dir, "diff", "--name-only", w.Base+"..HEAD")
	if err != nil || out == "" {
		return nil
	}
	var bad []string
	for _, p := range strings.Split(out, "\n") {
		if w.isSynced(p) {
			bad = append(bad, p)
		}
	}
	return bad
}

// isSynced reports whether a repository-relative path is, or lies under, a
// synced entry.
func (w *Workspace) isSynced(p string) bool {
	if w.placed[p] {
		return true
	}
	for _, rel := range w.synced {
		if p == rel || strings.HasPrefix(p, rel+"/") {
			return true
		}
	}
	return false
}

// SyncPermits is the permission rules that lift a worker's deny on writing the
// write-access entries of specs, and nothing else.
func SyncPermits(specs []agentprofile.SyncSpec) []string {
	var out []string
	for _, s := range specs {
		if !s.Writes() {
			continue
		}
		subject := s.Clean()
		if s.IsDir() {
			subject += "/*"
		}
		out = append(out, "Write("+subject+")", "Edit("+subject+")")
	}
	return out
}

// listSyncSources lists the regular files an entry names under the repository:
// the file itself, or every file below a directory. A path that does not exist
// names nothing; a symlink anywhere on the way is an error.
func listSyncSources(repo, rel string) ([]string, error) {
	return listFiles(repo, rel)
}

// listWorktreeFiles lists the files an entry has in the worktree, including
// ones the worker created.
func listWorktreeFiles(dir, rel string) ([]string, error) {
	return listFiles(dir, rel)
}

func listFiles(root, rel string) ([]string, error) {
	if err := noSymlinks(root, rel); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode().IsRegular() {
		return []string{rel}, nil
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is neither a file nor a directory", rel)
	}
	var out []string
	var walk func(r string) error
	walk = func(r string) error {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(r)))
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			child := path.Join(r, name)
			switch {
			case strings.HasSuffix(name, ".part"):
				// a copy in flight
			case e.Type()&os.ModeSymlink != 0:
				return fmt.Errorf("%s is a symlink", child)
			case e.IsDir():
				if err := walk(child); err != nil {
					return err
				}
			case e.Type().IsRegular():
				if info, ierr := e.Info(); ierr == nil {
					if why, ok := locate.EligibleFile(name, info.Size()); !ok && why == locate.SkipSensitive {
						continue
					}
				}
				out = append(out, child)
				if len(out) > maxSyncFiles {
					return fmt.Errorf("%s holds more than %d files", rel, maxSyncFiles)
				}
			}
		}
		return nil
	}
	if err := walk(rel); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// noSymlinks checks that no component of rel under root is a symlink. A
// missing component is os.ErrNotExist.
func noSymlinks(root, rel string) error {
	cur := root
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", cur)
		}
	}
	return nil
}

// ensureDirs and writeSyncFile are knowledge.EnsureDirs and
// knowledge.WriteFileAtomic: no symlink on the way, atomic replace.
func ensureDirs(root, rel string) error { return knowledge.EnsureDirs(root, rel) }

// readSyncFile reads a regular, non-symlink text file within the size limit.
func readSyncFile(p string) ([]byte, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxSyncFileBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxSyncFileBytes)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSyncFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSyncFileBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxSyncFileBytes)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("not text")
	}
	return data, nil
}

func writeSyncFile(p string, data []byte) error { return knowledge.WriteFileAtomic(p, data) }

// lockSyncFile takes the cross-process lock for one repository file. The
// lockfile lives in the state directory, never in the repository.
func lockSyncFile(repo, rel string) (func(), error) {
	agents, err := config.AgentsDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(agents, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(repo) + "\x00" + rel))
	return config.AcquireFileLock(filepath.Join(dir, "sync-"+hex.EncodeToString(sum[:16])+".lock"))
}
