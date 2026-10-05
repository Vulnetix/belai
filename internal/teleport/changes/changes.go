// Package changes moves a working tree's code between two hosts when a teleport
// cannot coordinate over the forge. The origin host reads what its session
// changed (commits not yet pushed, plus uncommitted work) into a Snapshot: the
// last pushed commit it builds on, the tree object of the final state and one
// unified patch between them. The target host checks the patch strictly
// (ParsePatch), applies it file by file with git (Apply), and compares the tree
// it ends with to the origin's (Matches), which is an exact verification that
// needs no model.
//
// Nothing here trusts a patch. It arrives from another host through the
// backend, so it is parsed against a closed grammar (no rename, copy, binary
// patch, symlink, submodule or mode other than 100644 and 100755, and no path
// outside the repository, in .git or in Belai's own state), bounded in size and
// count, and applied by git inside a worktree the teleport made, never the
// checkout the user is in. The origin leaves out what must not travel: a
// credential-bearing file, a binary, an oversized file, a symlink and anything
// git ignores. Git runs hardened (hooks and fsmonitor off, no file transport,
// scrubbed environment) through forge.HardenedGit. docs/teleport.md.
package changes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/locate"
)

// Bounds on what one teleport carries. They are the harness's, not a setting:
// a patch is read by a model on the other side, so it has to fit.
const (
	// MaxPatchBytes bounds the unified patch.
	MaxPatchBytes = 512 << 10
	// MaxFileBytes bounds one changed file's size on the origin.
	MaxFileBytes = 256 << 10
	// MaxFiles bounds how many files one patch changes.
	MaxFiles = 200
	// maxStatusEntries bounds the working tree entries read from git status.
	maxStatusEntries = 5000
)

// Why a changed file was left out of the patch, as it is reported to the user.
const (
	SkipSensitive = "credentials"
	SkipBinary    = "binary"
	SkipLarge     = "too large"
	SkipSymlink   = "symlink"
	SkipSpecial   = "not a regular file"
	SkipPath      = "unusual path"
)

// Refusals, so the caller can say why there is nothing to replay.
var (
	// ErrNothing means the tree matches the last pushed commit: there is
	// nothing to move.
	ErrNothing = errors.New("nothing to move: the working tree matches the last pushed commit")
	// ErrNoBase means no pushed commit this one builds on could be found, so a
	// target has nothing to start from.
	ErrNoBase = errors.New("no pushed commit to build on: the branch has no remote-tracking base")
	// ErrTooLarge means the patch is bigger than a teleport carries.
	ErrTooLarge = errors.New("the changes are larger than a teleport carries")
	// ErrTooMany means the patch changes more files than a teleport carries.
	ErrTooMany = errors.New("the changes touch more files than a teleport carries")
)

// File is one file the patch changes.
type File struct {
	Path string `json:"path"`
	// Status is added, modified or deleted.
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Skip is a changed file left out of the patch, and why.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Snapshot is a working tree's changes, ready to move. Base is a commit the
// target can reach on the forge; Patch turns Base's tree into Tree.
type Snapshot struct {
	// Head is the commit the origin had checked out; Base the last pushed
	// commit it builds on; Tree the tree object of the final state.
	Head, Base, Tree string
	Branch           string
	// Commits is how many commits Head has that Base does not.
	Commits int
	// Dirty is whether the working tree had uncommitted changes.
	Dirty   bool
	Patch   string
	Files   []File
	Skipped []Skip
}

// Runners builds a git runner whose environment carries extra entries. The
// default (HardenedGit) is the only one production uses; tests may pass their own.
type Runners func(env ...string) forge.Runner

// Hardened is the production Runners: every call hardened, no repository pinned.
func Hardened(env ...string) forge.Runner { return forge.HardenedGit("", "", env...) }

var (
	shaShape = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	// pathShape is the alphabet a patched path may use. A path outside it (a
	// space, a quote, a control or non-ASCII rune) would be quoted by git, so it
	// is left out at the origin rather than parsed on the target.
	pathShape = regexp.MustCompile(`^[A-Za-z0-9._@+/-]{1,400}$`)
)

// SafePath reports whether p can be a path in a teleported patch: relative, plain
// characters, no empty or dot component, and nothing inside .git or Belai's own
// .vulnetix directory. The check is the same on both hosts.
func SafePath(p string) bool {
	if !pathShape.MatchString(p) || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		switch strings.ToLower(part) {
		case "", ".", "..", ".git", ".vulnetix":
			return false
		}
		if len(part) > 255 {
			return false
		}
	}
	return true
}

func run(ctx context.Context, r forge.Runner, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := r(ctx, dir, append([]string{"git"}, args...)...)
	return strings.TrimSpace(string(out)), err
}

func runRaw(ctx context.Context, r forge.Runner, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return r(ctx, dir, append([]string{"git"}, args...)...)
}

// Collect reads dir's changes since the last pushed commit: its unpushed
// commits and its uncommitted tracked and untracked files, after leaving out
// what must not travel. It never changes the working tree, the real index or
// any ref: the final state is built in a throwaway index. ErrNothing means there
// is nothing to move; ErrNoBase, ErrTooLarge and ErrTooMany say why a replay
// cannot be made.
func Collect(ctx context.Context, mk Runners, dir string) (Snapshot, error) {
	if mk == nil {
		mk = Hardened
	}
	g := mk("GIT_OPTIONAL_LOCKS=0")
	root, err := run(ctx, g, dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return Snapshot{}, fmt.Errorf("not a git repository: %s", forge.CleanErr(err))
	}
	head, err := run(ctx, g, root, "rev-parse", "HEAD")
	if err != nil || !shaShape.MatchString(head) {
		return Snapshot{}, errors.New("the repository has no commit yet")
	}
	s := Snapshot{Head: head}
	s.Branch, _ = run(ctx, g, root, "branch", "--show-current")
	if s.Base, err = pushedBase(ctx, g, root); err != nil {
		return s, err
	}
	if n, err := run(ctx, g, root, "rev-list", "--count", s.Base+"..HEAD"); err == nil {
		s.Commits, _ = strconv.Atoi(n)
	}

	tree, skipped, changed, err := WorkTree(ctx, mk, root)
	if err != nil {
		return s, err
	}
	s.Tree, s.Skipped, s.Dirty = tree, skipped, changed > 0

	baseTree, err := run(ctx, g, root, "rev-parse", s.Base+"^{tree}")
	if err != nil {
		return s, fmt.Errorf("read the base tree: %s", forge.CleanErr(err))
	}
	if baseTree == tree {
		return s, ErrNothing
	}
	raw, err := runRaw(ctx, g, root, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--full-index",
		"--src-prefix=a/", "--dst-prefix=b/", s.Base, tree)
	if err != nil {
		return s, fmt.Errorf("diff: %s", forge.CleanErr(err))
	}
	if len(raw) > MaxPatchBytes {
		return s, ErrTooLarge
	}
	s.Patch = string(raw)
	if s.Patch == "" {
		return s, ErrNothing
	}
	secs, err := ParsePatch(s.Patch)
	if err != nil {
		return s, fmt.Errorf("the changes cannot be sent: %w", err)
	}
	if len(secs) > MaxFiles {
		return s, ErrTooMany
	}
	s.Files = fileStats(ctx, g, root, s.Base, tree, secs)
	return s, nil
}

// pushedBase finds the commit the head builds on that the forge has: the merge
// base with the branch's upstream, else the nearest of origin's remote-tracking
// branches (fewest commits between it and HEAD).
func pushedBase(ctx context.Context, g forge.Runner, root string) (string, error) {
	if up, err := run(ctx, g, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil && up != "" {
		if mb, err := run(ctx, g, root, "merge-base", "HEAD", "refs/remotes/"+strings.TrimPrefix(up, "refs/remotes/")); err == nil && shaShape.MatchString(mb) {
			return mb, nil
		}
		if mb, err := run(ctx, g, root, "merge-base", "HEAD", up); err == nil && shaShape.MatchString(mb) {
			return mb, nil
		}
	}
	list, err := run(ctx, g, root, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/")
	if err != nil {
		return "", ErrNoBase
	}
	best, bestN := "", -1
	for i, ref := range strings.Split(list, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" || strings.HasSuffix(ref, "/HEAD") || i >= 50 {
			continue
		}
		mb, err := run(ctx, g, root, "merge-base", "HEAD", ref)
		if err != nil || !shaShape.MatchString(mb) {
			continue
		}
		n, err := run(ctx, g, root, "rev-list", "--count", mb+"..HEAD")
		if err != nil {
			continue
		}
		c, _ := strconv.Atoi(n)
		if bestN < 0 || c < bestN {
			best, bestN = mb, c
		}
	}
	if best == "" {
		return "", ErrNoBase
	}
	return best, nil
}

// WorkTree builds the tree object of root's working tree as it would be sent:
// HEAD's tree with every eligible tracked or untracked change applied, in a
// throwaway index, so neither the real index nor any file is touched. Skipped
// lists what was left out; changed counts the entries git reported (eligible or
// not). The target runs the same function to compare.
func WorkTree(ctx context.Context, mk Runners, root string) (tree string, skipped []Skip, changed int, err error) {
	tmp, err := os.MkdirTemp("", "belai-teleport-")
	if err != nil {
		return "", nil, 0, err
	}
	defer os.RemoveAll(tmp)
	idx := filepath.Join(tmp, "index")
	g := mk("GIT_OPTIONAL_LOCKS=0")
	x := mk("GIT_INDEX_FILE=" + idx)
	if _, err := run(ctx, x, root, "read-tree", "HEAD"); err != nil {
		return "", nil, 0, fmt.Errorf("read HEAD: %s", forge.CleanErr(err))
	}
	raw, err := runRaw(ctx, g, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return "", nil, 0, fmt.Errorf("status: %s", forge.CleanErr(err))
	}
	var eligible []string
	seen := map[string]bool{}
	for _, ent := range strings.Split(string(raw), "\x00") {
		if len(ent) < 4 || ent[2] != ' ' {
			continue
		}
		p := ent[3:]
		if seen[p] {
			continue
		}
		seen[p] = true
		changed++
		if changed > maxStatusEntries {
			return "", nil, changed, ErrTooMany
		}
		if why := classify(root, p); why != "" {
			skipped = append(skipped, Skip{Path: displayPath(p), Reason: why})
			continue
		}
		eligible = append(eligible, p)
	}
	sort.Strings(eligible)
	for i := 0; i < len(eligible); i += 64 {
		batch := eligible[i:min(i+64, len(eligible))]
		args := append([]string{"--literal-pathspecs", "add", "-A", "--"}, batch...)
		if _, err := run(ctx, x, root, args...); err != nil {
			return "", nil, changed, fmt.Errorf("stage: %s", forge.CleanErr(err))
		}
	}
	tree, err = run(ctx, x, root, "write-tree")
	if err != nil || !shaShape.MatchString(tree) {
		return "", nil, changed, fmt.Errorf("write tree: %s", forge.CleanErr(err))
	}
	return tree, skipped, changed, nil
}

// displayPath is a path as it is reported: cleaned and capped, never trusted.
func displayPath(p string) string {
	p = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, p)
	if len(p) > 200 {
		p = p[:200] + "…"
	}
	return p
}

// classify says why a changed path must not be sent, or "" when it can be. It
// reads only the path's name, type and size and the start of its bytes.
func classify(root, rel string) string {
	if !SafePath(rel) {
		return SkipPath
	}
	if why, ok := locate.EligibleFile(path.Base(rel), 0); !ok && why != locate.SkipHidden {
		if why == locate.SkipSensitive {
			return SkipSensitive
		}
		if why == locate.SkipBinary {
			return SkipBinary
		}
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(full)
	switch {
	case err != nil:
		// A deleted file: the deletion travels, unless the file was a credential store.
		return ""
	case fi.Mode()&os.ModeSymlink != 0:
		return SkipSymlink
	case !fi.Mode().IsRegular():
		return SkipSpecial
	case fi.Size() > MaxFileBytes:
		return SkipLarge
	}
	f, err := os.Open(full)
	if err != nil {
		return SkipSpecial
	}
	defer f.Close()
	head := make([]byte, 8000)
	n, _ := f.Read(head)
	if !locate.IsText(head[:n]) {
		return SkipBinary
	}
	return ""
}

// fileStats adds the added and removed line counts git reports to each section.
func fileStats(ctx context.Context, g forge.Runner, root, base, tree string, secs []Section) []File {
	counts := map[string][2]int{}
	if raw, err := runRaw(ctx, g, root, "diff", "--numstat", "-z", "--no-renames", base, tree); err == nil {
		for _, ent := range strings.Split(string(raw), "\x00") {
			parts := strings.SplitN(ent, "\t", 3)
			if len(parts) != 3 {
				continue
			}
			a, _ := strconv.Atoi(parts[0])
			r, _ := strconv.Atoi(parts[1])
			counts[parts[2]] = [2]int{a, r}
		}
	}
	out := make([]File, 0, len(secs))
	for _, s := range secs {
		c := counts[s.Path]
		out = append(out, File{Path: s.Path, Status: s.Status, Added: c[0], Removed: c[1]})
	}
	return out
}
