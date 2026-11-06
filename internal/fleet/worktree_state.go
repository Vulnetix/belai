package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/kanban"
)

// The records of the worktrees the harness made (docs/worktrees.md). A record
// holds facts the harness wrote: an id, the card it belongs to, the branch, the
// base commit and ref, and a time. It holds no path (the directory is rebuilt
// from the id), no output and no model text. It is the harness's own map of
// what it may touch: /worktree list, update and drop act only on a worktree
// that has a record and that the repository still lists under that name.

// WorktreeRecord is one worktree the harness made.
type WorktreeRecord struct {
	// ID is the worktree directory's name: K-xxxxxx-yyyyyy.
	ID string `json:"id"`
	// Item is the card's short id, K-xxxxxx.
	Item string `json:"item"`
	// Branch is the branch checked out in it.
	Branch string `json:"branch"`
	// Base is the commit the work is measured from; BaseRef the branch it came
	// from, when there was one.
	Base    string `json:"base"`
	BaseRef string `json:"base_ref,omitempty"`
	// Kept is true for a worktree opened with /worktree new, which stays after
	// its agent finishes (worktrees.keep_on_exit).
	Kept bool `json:"kept,omitempty"`
	// Created is when the record was first written, in unix milliseconds.
	Created int64 `json:"created"`

	// Dir and Exists are filled when records are listed; they are never read
	// from the file.
	Dir    string `json:"-"`
	Exists bool   `json:"-"`
}

var (
	worktreeIDRE = regexp.MustCompile(`^K-[0-9a-f]{6}-[0-9a-f]{6}$`)
	shortIDRE    = regexp.MustCompile(`^K-[0-9a-f]{6}$`)
	branchRE     = regexp.MustCompile(`^belai/[A-Za-z0-9._-]{1,64}(/[A-Za-z0-9._-]{1,64}){0,3}$`)
	commitRE     = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// maxRecordBytes bounds a record file read back.
const maxRecordBytes = 16 << 10

// ValidWorktreeID reports whether s names a worktree the harness could have made.
func ValidWorktreeID(s string) bool { return worktreeIDRE.MatchString(s) }

func (r WorktreeRecord) valid() bool {
	return ValidWorktreeID(r.ID) && shortIDRE.MatchString(r.Item) && strings.HasPrefix(r.ID, r.Item+"-") &&
		branchRE.MatchString(r.Branch) && !strings.Contains(r.Branch, "..") &&
		commitRE.MatchString(r.Base) && r.Created > 0
}

// recordDir is the state directory of the repository's records.
func recordDir(repo string) (string, string, error) {
	_, key := kanban.ProjectFor(repo)
	dir, err := config.WorktreeStateDir(key)
	return dir, key, err
}

// worktreeDir is where a worktree with this id lives: rebuilt from the id and
// the repository, never read from a record.
func worktreeDir(repo, id string) (string, error) {
	if !ValidWorktreeID(id) {
		return "", fmt.Errorf("%q is not a worktree id", id)
	}
	root, err := config.WorktreesDir()
	if err != nil {
		return "", err
	}
	_, key := kanban.ProjectFor(repo)
	return filepath.Join(root, key, id), nil
}

func (w *Workspace) recordCreated() (int64, bool) {
	rec, err := readRecord(w.repo, w.id)
	if err != nil {
		return 0, false
	}
	return rec.Created, true
}

// SaveRecord writes this worktree's record: atomically, mode 0600, under the
// record lock. Best effort for the caller: a worktree without a record still
// works, it is only missing from /worktree list.
func (w *Workspace) SaveRecord(kept bool) error {
	if !w.Worktree || !ValidWorktreeID(w.id) {
		return nil
	}
	rec := WorktreeRecord{
		ID: w.id, Item: w.id[:8], Branch: w.Branch, Base: w.Base, BaseRef: w.BaseRef, Kept: kept,
		Created: time.Now().UnixMilli(),
	}
	if created, ok := w.recordCreated(); ok {
		rec.Created = created
	}
	return writeRecord(w.repo, rec)
}

func writeRecord(repo string, rec WorktreeRecord) error {
	if !rec.valid() {
		return errors.New("worktree record is not valid")
	}
	dir, _, err := recordDir(repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := config.AcquireFileLock(filepath.Join(dir, rec.ID+".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	final := filepath.Join(dir, rec.ID+".json")
	if fi, err := os.Lstat(final); err == nil && !fi.Mode().IsRegular() {
		return errors.New("worktree record is not a regular file")
	}
	tmp, err := os.CreateTemp(dir, ".rec-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, final)
}

func readRecord(repo, id string) (WorktreeRecord, error) {
	if !ValidWorktreeID(id) {
		return WorktreeRecord{}, fmt.Errorf("%q is not a worktree id", id)
	}
	dir, _, err := recordDir(repo)
	if err != nil {
		return WorktreeRecord{}, err
	}
	path := filepath.Join(dir, id+".json")
	fi, err := os.Lstat(path)
	if err != nil {
		return WorktreeRecord{}, err
	}
	if !fi.Mode().IsRegular() {
		return WorktreeRecord{}, errors.New("worktree record is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return WorktreeRecord{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return WorktreeRecord{}, err
	}
	if len(data) > maxRecordBytes {
		return WorktreeRecord{}, errors.New("worktree record is too large")
	}
	var rec WorktreeRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return WorktreeRecord{}, err
	}
	// The file name is the id; a record that names another is not this one.
	if rec.ID != id || !rec.valid() {
		return WorktreeRecord{}, errors.New("worktree record is not valid")
	}
	return rec, nil
}

// dropRecord removes this worktree's record, best effort.
func (w *Workspace) dropRecord() {
	if !w.Worktree || !ValidWorktreeID(w.id) {
		return
	}
	dir, _, err := recordDir(w.repo)
	if err != nil {
		return
	}
	unlock, err := config.AcquireFileLock(filepath.Join(dir, w.id+".lock"))
	if err != nil {
		return
	}
	defer unlock()
	_ = os.Remove(filepath.Join(dir, w.id+".json"))
}

// WorktreeRecords lists the records of the repository's worktrees, newest
// first. A file that is not a valid record is skipped and left alone. Exists
// says whether the directory is still there.
func WorktreeRecords(repo string) ([]WorktreeRecord, error) {
	dir, _, err := recordDir(repo)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []WorktreeRecord
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidWorktreeID(id) {
			continue
		}
		rec, err := readRecord(repo, id)
		if err != nil {
			continue
		}
		if d, err := worktreeDir(repo, id); err == nil {
			rec.Dir = d
			if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
				rec.Exists = true
			}
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out, nil
}

// OpenWorktree reattaches to a worktree the harness made earlier, for an
// update or a removal. It trusts nothing it reads back: the directory is rebuilt
// from the id, must be a real directory under the worktrees directory, and must
// be one the repository itself lists as a worktree on the record's branch. The
// .git pointer is read now and pinned, as when the worktree was made.
func OpenWorktree(ctx context.Context, repo, id string) (*Workspace, error) {
	repoRun := hardenedGit("", "")
	top, err := git(ctx, repoRun, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("worktree: %s is not a git repository", repo)
	}
	rec, err := readRecord(top, id)
	if err != nil {
		return nil, fmt.Errorf("worktree %s: no record of it for this repository", id)
	}
	dir, err := worktreeDir(top, id)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("worktree %s: its directory is gone", id)
	}
	ident := identityEnv(ctx, repoRun, top)
	repoRun = hardenedGit("", "", ident...)
	listed, err := git(ctx, repoRun, top, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	found := false
	for _, wt := range forge.ParseWorktrees(listed) {
		p, _ := filepath.EvalSymlinks(wt.Path)
		if (wt.Path == dir || (real != "" && p == real)) && wt.Branch == rec.Branch {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("worktree %s: the repository does not list it on branch %s", id, rec.Branch)
	}
	commonDir, err := git(ctx, repoRun, top, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(top, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	dotgit, gitDir, err := readDotGit(dir)
	if err != nil {
		return nil, err
	}
	gitDir = filepath.Clean(gitDir)
	// The admin directory must be one of this repository's own.
	if rel, err := filepath.Rel(filepath.Join(commonDir, "worktrees"), gitDir); err != nil || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) {
		return nil, fmt.Errorf("worktree %s: its git file does not point into this repository", id)
	}
	return &Workspace{
		Dir: dir, Branch: rec.Branch, Worktree: true, Base: rec.Base, BaseRef: rec.BaseRef, Resumed: true, id: id,
		repo: top, gitDir: gitDir, commonDir: commonDir, dotgit: dotgit,
		run: hardenedGit(gitDir, dir, ident...), repoRun: repoRun, ident: ident,
	}, nil
}

// updateRecordBase stores the base an update moved to, so a later listing and
// the next update start from it.
func (w *Workspace) updateRecordBase() {
	rec, err := readRecord(w.repo, w.id)
	if err != nil {
		return
	}
	rec.Base = w.Base
	_ = writeRecord(w.repo, rec)
}
