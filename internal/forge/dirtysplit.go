package forge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DirtyTimeout bounds one `git status` read for the commit workflow. A large
// tree needs more room than the panel's short reads.
const DirtyTimeout = 20 * time.Second

// MaxDirtyPaths is the most dirty paths one read accepts. A tree dirtier than
// this is a cache or a build output, and the commit workflow refuses to guess.
const MaxDirtyPaths = 20000

// MaxCommitFileBytes is the largest file the commit workflow stages. A bigger
// one is withheld and reported, never committed.
const MaxCommitFileBytes = 8 << 20

// Split is the commit workflow's decision about every path that is dirty after
// a turn. Only Task is ever staged. Every list is sorted and slash separated,
// relative to the working directory.
type Split struct {
	// Task are paths the turn changed that were clean when it began.
	Task []string
	// Mixed are paths that were already dirty before the turn and that the turn
	// touched too. Their pre-existing edits cannot be told from the turn's, so
	// they are reported and never committed.
	Mixed []string
	// Withheld are task paths a harness rule refused: a credential store,
	// Belai's state or scan evidence, git's own files, a commit_exclude match,
	// or a file over MaxCommitFileBytes.
	Withheld []string
	// PreExisting are paths dirty before the turn that the turn did not touch.
	PreExisting []string
	// Unrelated are paths dirty now that were clean before the turn and that
	// the turn's tools did not report, such as an editor's save.
	Unrelated []string
}

// SplitOptions are the rules that withhold a task path.
type SplitOptions struct {
	// Exclude are commit_exclude globs (config.ValidCommitExclude shapes).
	Exclude []string
	// Withhold is a caller predicate, such as the credential-file check, that
	// forge cannot import itself.
	Withhold func(rel string) bool
	// Root, when set, lets the size cap read file sizes.
	Root string
	// MaxFileBytes overrides MaxCommitFileBytes when positive.
	MaxFileBytes int64
}

// DirtyPaths lists every path `git status` reports under root, relative to
// root, with a rename reported by both names and untracked files listed one by
// one. A tree with more than MaxDirtyPaths entries is an error, as is any git
// failure: the commit workflow fails closed on an unreadable status.
func DirtyPaths(ctx context.Context, r Runner, root string) ([]string, error) {
	if r == nil {
		r = ExecRunner
	}
	prefix, err := run(ctx, r, ReadTimeout, root, "git", "rev-parse", "--show-prefix")
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	// Not trimmed: a status line begins with a significant space.
	tctx, cancel := context.WithTimeout(ctx, DirtyTimeout)
	defer cancel()
	out, err := r(tctx, root, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	return parseStatusZ(out, prefix)
}

// parseStatusZ reads `git status --porcelain=v1 -z`. Paths are repository
// relative, so prefix (the working directory below the repository root, with a
// trailing slash, or "") is stripped and a path outside it is dropped.
func parseStatusZ(out []byte, prefix string) ([]string, error) {
	seen := map[string]bool{}
	var paths []string
	add := func(p string) error {
		if prefix != "" {
			rest, ok := strings.CutPrefix(p, prefix)
			if !ok {
				return nil
			}
			p = rest
		}
		p = strings.TrimSuffix(p, "/")
		if p == "" || seen[p] {
			return nil
		}
		if len(seen) >= MaxDirtyPaths {
			return fmt.Errorf("the tree has more than %d dirty paths", MaxDirtyPaths)
		}
		seen[p] = true
		paths = append(paths, p)
		return nil
	}
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 || f[2] != ' ' {
			continue
		}
		if err := add(string(f[3:])); err != nil {
			return nil, err
		}
		// A rename or copy is followed by its origin as its own field.
		if f[0] == 'R' || f[0] == 'C' || f[1] == 'R' || f[1] == 'C' {
			if i+1 < len(fields) && len(fields[i+1]) > 0 {
				if err := add(string(fields[i+1])); err != nil {
					return nil, err
				}
			}
			i++
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// DirtySplit reads the tree's dirty paths now and splits them against the
// snapshot taken before the turn and the paths the turn's tools reported.
func DirtySplit(ctx context.Context, r Runner, root string, before, task []string, o SplitOptions) (Split, error) {
	now, err := DirtyPaths(ctx, r, root)
	if err != nil {
		return Split{}, err
	}
	if o.Root == "" {
		o.Root = root
	}
	return SplitDirty(before, now, task, o), nil
}

// SplitDirty is DirtySplit over already-read sets. It touches the disk only to
// read file sizes under o.Root.
func SplitDirty(before, now, task []string, o SplitOptions) Split {
	was := toSet(before)
	dirty := toSet(now)
	touched := toSet(task)
	limit := o.MaxFileBytes
	if limit <= 0 {
		limit = MaxCommitFileBytes
	}
	var s Split
	for p := range touched {
		switch {
		case !dirty[p]:
			// Unchanged, ignored or already committed: nothing to stage.
		case was[p]:
			s.Mixed = append(s.Mixed, p)
		case withheldPath(p, o, limit):
			s.Withheld = append(s.Withheld, p)
		default:
			s.Task = append(s.Task, p)
		}
	}
	for p := range dirty {
		if touched[p] {
			continue
		}
		if was[p] {
			s.PreExisting = append(s.PreExisting, p)
		} else {
			s.Unrelated = append(s.Unrelated, p)
		}
	}
	for _, l := range [][]string{s.Task, s.Mixed, s.Withheld, s.PreExisting, s.Unrelated} {
		sort.Strings(l)
	}
	return s
}

func toSet(l []string) map[string]bool {
	m := make(map[string]bool, len(l))
	for _, p := range l {
		m[filepath.ToSlash(p)] = true
	}
	return m
}

// withheldPath applies the harness rules to one task path.
func withheldPath(p string, o SplitOptions, limit int64) bool {
	if p == ".git" || strings.HasPrefix(p, ".git/") || strings.Contains(p, "/.git/") {
		return true
	}
	if o.Withhold != nil && o.Withhold(p) {
		return true
	}
	for _, g := range o.Exclude {
		if MatchExclude(g, p) {
			return true
		}
	}
	if o.Root != "" {
		if fi, err := os.Lstat(filepath.Join(o.Root, filepath.FromSlash(p))); err == nil && fi.Mode().IsRegular() && fi.Size() > limit {
			return true
		}
	}
	return false
}

// MatchExclude reports whether a commit_exclude glob names rel. A pattern with
// a trailing slash names a directory and everything under it. A pattern with
// no slash is matched against every path segment's base name, so "*.log" and
// "secret.txt" apply at any depth; one with a slash is matched against the
// whole path, a leading directory included.
func MatchExclude(glob, rel string) bool {
	dir := strings.HasSuffix(glob, "/")
	g := strings.TrimSuffix(glob, "/")
	if g == "" {
		return false
	}
	segs := strings.Split(rel, "/")
	if !strings.Contains(g, "/") {
		last := len(segs) - 1
		if dir {
			last-- // a directory pattern never matches the file itself
		}
		for i := 0; i <= last; i++ {
			if ok, _ := path.Match(g, segs[i]); ok {
				return true
			}
		}
		return false
	}
	for i := 1; i <= len(segs); i++ {
		if dir && i == len(segs) {
			break
		}
		if ok, _ := path.Match(g, strings.Join(segs[:i], "/")); ok {
			return true
		}
	}
	return false
}
