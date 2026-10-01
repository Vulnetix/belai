package fleet

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/knowledge"
)

// syncTestFiles are the test files that hold the sync behaviour. Every test in
// them must be named in the rules table of docs/fleet.md.
var syncTestFiles = []string{
	"internal/fleet/sync_test.go",
	"internal/agentprofile/sync_test.go",
	"internal/permissions/harness_test.go",
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func kib(n int) string { return fmt.Sprintf("%d KiB", n>>10) }

// TestSyncDocParity keeps docs/fleet.md and the sync tests in step: every rule
// (S) and edge case (X) names tests that exist, every sync test is named by the
// doc, and the limits the section states are the ones in the code.
func TestSyncDocParity(t *testing.T) {
	root := moduleRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "fleet.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	start := strings.Index(doc, "### Files placed in a worktree")
	end := strings.Index(doc, "### Acceptance gates")
	if start < 0 || end < start {
		t.Fatal("docs/fleet.md has no `Files placed in a worktree` section before `Acceptance gates`")
	}
	section := doc[start:end]

	rows := regexp.MustCompile(`(?m)^\| ((?:S|X)\d+) \|.*$`).FindAllStringSubmatch(section, -1)
	ids := map[string]bool{}
	for _, m := range rows {
		if ids[m[1]] {
			t.Errorf("%s is defined twice", m[1])
		}
		ids[m[1]] = true
		if !regexp.MustCompile("`Test\\w+`").MatchString(m[0]) {
			t.Errorf("%s names no test: %s", m[1], m[0])
		}
	}
	if len(ids) < 18 {
		t.Fatalf("found only %d rule and edge IDs; has the table format changed?", len(ids))
	}

	exists := map[string]bool{}
	testRe := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "site", "bin", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range testRe.FindAllStringSubmatch(string(src), -1) {
			exists[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	var missing []string
	for _, row := range rows {
		for _, m := range regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(row[0], -1) {
			named[m[1]] = true
			if !exists[m[1]] {
				missing = append(missing, row[1]+": "+m[1])
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("tests the doc names that do not exist: %v", missing)
	}
	var unnamed []string
	for _, rel := range syncTestFiles {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		for _, m := range testRe.FindAllStringSubmatch(string(src), -1) {
			if !named[m[1]] {
				unnamed = append(unnamed, rel+": "+m[1])
			}
		}
	}
	sort.Strings(unnamed)
	if len(unnamed) > 0 {
		t.Errorf("sync tests docs/fleet.md does not name (add a rule or edge case): %v", unnamed)
	}

	// The limits the section states are the code's.
	for _, want := range []string{
		kib(maxSyncFileBytes), fmt.Sprintf("%d files", maxSyncFiles), fmt.Sprintf("%d MiB", maxSyncTotalBytes>>20),
		fmt.Sprintf("up to %d repository-relative paths", agentprofile.MaxSyncPaths),
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the section does not state %q", want)
		}
	}
}

// TestPlacedDocumentLimitsAndFloorAreDocumented checks that docs/knowledge.md
// states the copy limits and the floor the code enforces.
func TestPlacedDocumentLimitsAndFloorAreDocumented(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(moduleRoot(t), "docs", "knowledge.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, want := range []string{
		fmt.Sprintf("%d files, %d MiB a file, %d MiB in all", knowledge.MaxCopyFiles, knowledge.MaxCopyFile>>20, knowledge.MaxCopyBytes>>20),
		knowledge.KnowledgeCopyDir,
		"`~/.ssh`", "`~/.gnupg`", "`~/.aws`", "`~/.kube`", "`~/.docker`", "`~/.config/gcloud`", "`~/.config/gh`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/knowledge.md does not state %q", want)
		}
	}
}
