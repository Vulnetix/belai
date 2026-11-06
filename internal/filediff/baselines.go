package filediff

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Limits for the baseline capture that file checkpoints (docs/checkpoints.md)
// rely on. They bound what a Bash call costs, whether or not it changes a file.
const (
	// DefaultBaselineMaxBytes is the largest earlier file kept for one path.
	DefaultBaselineMaxBytes = 20 << 20
	// DefaultBaselineBudget is the most earlier bytes held for one call.
	DefaultBaselineBudget = 64 << 20
	// MaxBaselinePaths is the most changed paths one call reports.
	MaxBaselinePaths = 500
	// maxUntrackedCapture is the most untracked files read before a call.
	maxUntrackedCapture = 200
)

// Baseline is what one path held before a command ran.
type Baseline struct {
	// Path is absolute.
	Path string
	// Existed is false for a file the command created.
	Existed bool
	// Known is true when Body is the whole earlier content.
	Known bool
	Body  []byte
	// Mode is the earlier permission bits, 0 when unknown.
	Mode fs.FileMode
	// Why says why Body is unknown, in harness words.
	Why string
}

// BaselineSet is the answer of [Snapshot.Baselines].
type BaselineSet struct {
	Items []Baseline
	// Outside counts changed paths outside the working directory, not listed.
	Outside int
	// Overflow counts changed paths past MaxBaselinePaths, not listed.
	Overflow int
	// Unavailable is why the snapshot could not see the command's effects, or "".
	Unavailable string
}

// statKey is what a file whose bytes were not kept looked like before the
// command, so a change to it is still noticed.
type statKey struct {
	size int64
	mod  int64
}

func statKeyOf(fi fs.FileInfo) statKey { return statKey{fi.Size(), fi.ModTime().UnixNano()} }

// captureBaselines reads, before the command runs, the files git already shows
// as changed, so that a command that rewrites or removes one leaves its earlier
// bytes behind. A clean tracked file needs nothing: the index holds it.
func (r *Recorder) captureBaselines(s *Snapshot, status map[string]string) {
	s.bodies = map[string][]byte{}
	s.bmodes = map[string]fs.FileMode{}
	s.bnote = map[string]string{}
	s.bstat = map[string]statKey{}
	paths := make([]string, 0, len(status))
	for p := range status {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var used int64
	untracked := 0
	for _, p := range paths {
		code := status[p]
		if strings.Contains(code, "D") && !strings.HasPrefix(code, "??") {
			// Already deleted in the worktree: nothing there to keep.
			if _, err := os.Lstat(filepath.Join(r.RepoRoot, p)); err != nil {
				continue
			}
		}
		abs := filepath.Join(r.RepoRoot, p)
		fi, err := os.Lstat(abs)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		note := func(why string) {
			s.bnote[p] = why
			s.bstat[p] = statKeyOf(fi)
		}
		if code == "??" {
			untracked++
			if untracked > maxUntrackedCapture {
				note("not captured: too many untracked files")
				continue
			}
		}
		if fi.Size() > r.BaselineMaxBytes {
			note("over the per-file size limit")
			continue
		}
		if used+fi.Size() > r.BaselineBudget {
			note("not captured: too many bytes in one command")
			continue
		}
		body, err := os.ReadFile(abs)
		if err != nil {
			note("could not be read")
			continue
		}
		used += int64(len(body))
		s.bodies[p] = body
		s.bmodes[p] = fi.Mode().Perm()
	}
}

// Baselines reports, after the command ran, each path it changed together with
// what the path held before. It needs Recorder.Baselines on when the snapshot
// was taken. A nil Snapshot answers with an empty set.
func (s *Snapshot) Baselines(ctx context.Context) BaselineSet {
	if s == nil {
		return BaselineSet{}
	}
	if s.unavailable != "" {
		return BaselineSet{Unavailable: s.unavailable}
	}
	if s.before != nil {
		return s.baselinesGit(ctx)
	}
	return s.baselinesHeuristic()
}

func (s *Snapshot) baselinesGit(ctx context.Context) BaselineSet {
	r := s.rec
	after, err := r.gitStatus(ctx)
	if err != nil {
		return BaselineSet{Unavailable: err.Error()}
	}
	cand := map[string]bool{}
	for p, code := range after {
		if before, ok := s.before[p]; !ok || before != code {
			cand[p] = true
		} else if _, ok := s.bodies[p]; ok {
			cand[p] = true // same status, maybe new bytes
		} else if was, ok := s.bstat[p]; ok {
			// Same status, earlier bytes not kept: only a changed size or time
			// says the command touched it.
			if fi, err := os.Lstat(filepath.Join(r.RepoRoot, p)); err != nil || statKeyOf(fi) != was {
				cand[p] = true
			}
		}
	}
	for p := range s.before {
		if _, ok := after[p]; !ok {
			cand[p] = true
		}
	}
	paths := make([]string, 0, len(cand))
	for p := range cand {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var set BaselineSet
	for _, p := range paths {
		abs := filepath.Join(r.RepoRoot, p)
		if !r.within(abs) {
			set.Outside++
			continue
		}
		if len(set.Items) >= MaxBaselinePaths {
			set.Overflow++
			continue
		}
		b, changed := s.baselineGit(ctx, p, abs, after)
		if changed {
			set.Items = append(set.Items, b)
		}
	}
	return set
}

// baselineGit resolves one candidate path; changed is false when its bytes are
// the same as before.
func (s *Snapshot) baselineGit(ctx context.Context, p, abs string, after map[string]string) (Baseline, bool) {
	r := s.rec
	b := Baseline{Path: abs}
	cur, curOK := readCapped(abs, r.BaselineMaxBytes)
	curExists := false
	if fi, err := os.Lstat(abs); err == nil && !fi.IsDir() {
		curExists = true
		b.Mode = fi.Mode().Perm()
	}
	before, wasKnown := s.before[p]

	if !wasKnown && after[p] == "??" {
		return b, true // the command created it
	}
	if body, ok := s.bodies[p]; ok {
		// Already changed before the command, and read then.
		b.Existed, b.Known, b.Body = true, true, body
		if m, ok := s.bmodes[p]; ok {
			b.Mode = m
		}
		return b, !(curOK && cur == string(body))
	}
	if note, ok := s.bnote[p]; ok {
		b.Existed, b.Why = true, note
		return b, true
	}
	if wasKnown {
		if before != "??" && before[1] == 'D' {
			// Already deleted in the worktree: the command can only have made it.
			b.Existed = false
			return b, curExists
		}
		b.Existed, b.Why = true, "its earlier content was not captured"
		return b, true
	}
	// A clean tracked file: the index still holds what the worktree held.
	old, ok := r.gitShowIndexCapped(ctx, p, r.BaselineMaxBytes)
	if !ok {
		b.Existed, b.Why = true, "over the per-file size limit"
		return b, true
	}
	b.Existed, b.Known, b.Body = true, true, []byte(old)
	if !curExists {
		b.Mode = r.indexMode(ctx, p)
	}
	return b, !(curOK && cur == old)
}

func (s *Snapshot) baselinesHeuristic() BaselineSet {
	var set BaselineSet
	for _, abs := range s.targets {
		b := Baseline{Path: abs, Existed: true}
		if fi, err := os.Lstat(abs); err == nil {
			b.Mode = fi.Mode().Perm()
		}
		if note, ok := s.bnote[abs]; ok {
			b.Why = note
			set.Items = append(set.Items, b)
			continue
		}
		old := s.existed[abs]
		b.Known, b.Body = true, []byte(old)
		if cur, ok := readCapped(abs, heuristicMaxBytes); ok && cur == old {
			continue
		}
		set.Items = append(set.Items, b)
	}
	for _, abs := range s.absent {
		if isRegular(abs) {
			set.Items = append(set.Items, Baseline{Path: abs})
		}
	}
	return set
}

// gitShowIndexCapped is gitShowIndex with the caller's size bound.
func (r *Recorder) gitShowIndexCapped(ctx context.Context, path string, max int64) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, r.GitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", r.RepoRoot, "--no-optional-locks", "show", ":"+path)
	out, err := cmd.Output()
	if err != nil || int64(len(out)) > max {
		return "", false
	}
	return string(out), true
}

// indexMode reads a tracked file's mode from the index: 0755 for an executable
// entry, 0644 otherwise.
func (r *Recorder) indexMode(ctx context.Context, path string) fs.FileMode {
	ctx, cancel := context.WithTimeout(ctx, r.GitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", r.RepoRoot, "--no-optional-locks", "ls-files", "-s", "-z", "--", path)
	out, err := cmd.Output()
	if err == nil && bytes.HasPrefix(out, []byte("100755")) {
		return 0o755
	}
	return 0o644
}
