package knowledge

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// knowledgeTestFiles are the test files that hold knowledge behaviour. Every
// test in them must be named in docs/knowledge.md, so a new behaviour cannot
// land without a rule or edge case in the doc.
var knowledgeTestFiles = []string{
	"internal/knowledge/knowledge_test.go",
	"internal/knowledge/manager_test.go",
	"internal/knowledge/sources_test.go",
	"internal/knowledge/newest_test.go",
	"internal/knowledge/tagging_test.go",
	"internal/knowledge/catalog_test.go",
	"internal/knowledge/tags/tags_test.go",
	"internal/knowledge/kbgate/tagger_test.go",
	"internal/rolemanager/jev/topics_test.go",
	"internal/tui/knowledge_view_test.go",
	"internal/knowledge/kbgate/kbgate_test.go",
	"internal/config/knowledge_test.go",
	"internal/agentprofile/knowledge_test.go",
	"internal/agent/knowledge_test.go",
	"internal/tools/knowledge_test.go",
	"internal/tui/knowledge_test.go",
	"internal/tui/knowledge_settings_test.go",
	"cmd/belai/knowledgecmd_test.go",
	"e2e/knowledge_e2e_test.go",
	"internal/scanartifacts/records_test.go",
}

// TestKnowledgeDocParity keeps docs/knowledge.md and the tests in step: every
// rule (K) and edge case (E) in the doc names tests that exist, every
// knowledge test is named by the doc, and the table has the shape this test
// reads.
func TestKnowledgeDocParity(t *testing.T) {
	root := kbModuleRoot(t)
	doc, err := os.ReadFile(filepath.Join(root, "docs", "knowledge.md"))
	if err != nil {
		t.Fatal(err)
	}

	rows := regexp.MustCompile(`(?m)^\| ((?:K|E)\d+) \|.*$`).FindAllStringSubmatch(string(doc), -1)
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
	if len(ids) < 40 {
		t.Fatalf("found only %d rule and edge IDs; has the table format changed?", len(ids))
	}

	// Every test the doc names exists in some test file of the module.
	exists := map[string]bool{}
	testRe := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
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

	// Every knowledge test is named by the doc.
	var unnamed []string
	for _, rel := range knowledgeTestFiles {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		for _, m := range testRe.FindAllStringSubmatch(string(src), -1) {
			if !named[m[1]] && m[1] != "TestKnowledgeDocParity" {
				unnamed = append(unnamed, rel+": "+m[1])
			}
		}
	}
	sort.Strings(unnamed)
	if len(unnamed) > 0 {
		t.Errorf("knowledge tests docs/knowledge.md does not name (add a rule or edge case): %v", unnamed)
	}
}

func kbModuleRoot(t *testing.T) string {
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
