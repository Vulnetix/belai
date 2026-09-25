package forge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// maxCommitPaths caps the number of paths one auto-commit stages. It mirrors
// the TUI collector's cap and keeps a pathological turn's argv bounded.
const maxCommitPaths = 1000

// maxCommitBodyPaths caps how many changed paths are listed in a commit
// message body.
const maxCommitBodyPaths = 50

// maxCommitHeaderRunes caps the conventional-commit header length (type,
// colon, space and subject) at the widely used 72 characters.
const maxCommitHeaderRunes = 72

// CommitPaths stages and commits exactly the listed paths under root and
// returns the new HEAD short sha. It is the single place auto-commit mutates
// the repository, and it never pushes, never stages without a pathspec, and
// never passes --no-verify (the user's hooks run, and a hook failure is
// reported).
//
// An empty or no-op path set returns ("", nil) without creating a commit.
// Only relative paths lexically inside root are kept; absolute paths and any
// path containing ".." are dropped. Paths that git check-ignore reports are
// dropped before staging. `git commit --only` keeps unrelated content the
// user had already staged out of the commit.
func CommitPaths(ctx context.Context, r Runner, root string, paths []string, msg string) (string, error) {
	if r == nil {
		r = ExecRunner
	}
	rel, err := commitPathsInside(root, paths)
	if err != nil {
		return "", err
	}
	if len(rel) == 0 {
		return "", nil
	}

	kept := make([]string, 0, len(rel))
	for _, p := range rel {
		// check-ignore exits 0 for an ignored path; any error (including the
		// normal exit 1 for "not ignored") keeps the path.
		if _, err := run(ctx, r, ReadTimeout, root, "git", "check-ignore", "--", p); err == nil {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return "", nil
	}

	add := append([]string{"git", "add", "-A", "--"}, kept...)
	if _, err := run(ctx, r, WriteTimeout, root, add...); err != nil {
		return "", fmt.Errorf("stage: %w", err)
	}

	diff := append([]string{"git", "diff", "--cached", "--quiet", "--"}, kept...)
	if _, err := run(ctx, r, ReadTimeout, root, diff...); err == nil {
		// Nothing staged for these paths: no commit.
		return "", nil
	}

	commit := append([]string{"git", "commit", "--only", "-m", msg, "--"}, kept...)
	if _, err := run(ctx, r, WriteTimeout, root, commit...); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	sha, err := run(ctx, r, ReadTimeout, root, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	return sha, nil
}

// commitPathsInside normalises a path list to deduplicated, slash-separated
// paths relative to root, dropping absolute paths and any path containing
// "..". A path that still escapes root after joining is dropped too.
func commitPathsInside(root string, paths []string) ([]string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || filepath.IsAbs(p) || strings.Contains(p, "..") {
			continue
		}
		rel, err := filepath.Rel(absRoot, filepath.Join(absRoot, filepath.FromSlash(p)))
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

// TaskCommitMessage builds a conventional-commit message for a completed goal
// and the paths it changed. The type is decided from the paths and then the
// objective's first word; see commitType. The header (type plus first-line
// subject) is truncated to 72 characters, and the body lists at most 50 paths.
func TaskCommitMessage(objective string, paths []string) string {
	subject := commitSubject(objective)
	typ := commitType(objective, paths)
	header := typ + ": " + subject
	if n := len([]rune(header)); n > maxCommitHeaderRunes {
		header = string([]rune(header)[:maxCommitHeaderRunes])
	}

	var b strings.Builder
	b.WriteString(header)
	if len(paths) > 0 {
		b.WriteString("\n\n")
		limit := len(paths)
		if limit > maxCommitBodyPaths {
			limit = maxCommitBodyPaths
		}
		for _, p := range paths[:limit] {
			b.WriteString(p)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// commitType returns the conventional-commit type for a goal and its paths.
// Path-based types win over objective-prefix types, which win over the
// created/not-created fallback.
func commitType(objective string, paths []string) string {
	switch {
	case allPathsMatch(paths, isDocsPath):
		return "docs"
	case allPathsMatch(paths, isTestPath):
		return "test"
	case allPathsMatch(paths, isCIPath):
		return "ci"
	}

	first := strings.ToLower(firstWord(commitSubject(objective)))
	switch first {
	case "fix":
		return "fix"
	case "refactor":
		return "refactor"
	}

	for _, p := range paths {
		if pathCreated(p) {
			return "feat"
		}
	}
	return "chore"
}

// commitSubject returns the objective's first line with control characters
// stripped.
func commitSubject(objective string) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, objective)
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

func allPathsMatch(paths []string, fn func(string) bool) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if !fn(p) {
			return false
		}
	}
	return true
}

func isDocsPath(p string) bool {
	p = filepath.ToSlash(p)
	return strings.HasSuffix(p, ".md") || p == "docs" || strings.HasPrefix(p, "docs/")
}

func isTestPath(p string) bool {
	p = filepath.ToSlash(p)
	base := filepath.Base(p)
	return strings.Contains(base, "_test.") || p == "test" || strings.HasPrefix(p, "test/") ||
		p == "e2e" || strings.HasPrefix(p, "e2e/")
}

func isCIPath(p string) bool {
	p = filepath.ToSlash(p)
	return p == ".github" || strings.HasPrefix(p, ".github/")
}

// pathCreated reports whether p is a new (untracked) file in the current
// working directory's git repository. It is used only to choose feat versus
// chore, so a false answer costs the commit a slightly more generic type,
// never correctness.
func pathCreated(p string) bool {
	clean := filepath.Clean(filepath.FromSlash(p))
	if clean == "." || clean == "" || filepath.IsAbs(clean) || strings.Contains(clean, "..") {
		return false
	}
	if _, err := os.Stat(clean); err != nil {
		return false // deleted or never existed
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	cmd := exec.Command(git, "ls-files", "--error-unmatch", "--", clean)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return true // untracked
		}
		return false
	}
	return false // already tracked
}
