package budget

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestBudgetDocParity keeps docs/token-budgets.md and the tests in step: every
// business rule (R) and edge case (E) the doc defines has at least one test
// named TestBudgetRuleN_… or TestBudgetEdgeN_… somewhere in the module, and no
// test names an ID the doc does not define. Adding a rule to the doc without a
// test, or renumbering one side only, fails here.
func TestBudgetDocParity(t *testing.T) {
	root := moduleRoot(t)
	doc, err := os.ReadFile(filepath.Join(root, "docs", "token-budgets.md"))
	if err != nil {
		t.Fatal(err)
	}
	docIDs := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^- \*\*(R\d+)\.`).FindAllStringSubmatch(string(doc), -1) {
		docIDs[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`(?m)^\| (E\d+) \|`).FindAllStringSubmatch(string(doc), -1) {
		docIDs[m[1]] = true
	}
	if len(docIDs) < 20 {
		t.Fatalf("found only %d rule/edge IDs in the doc; has its format changed?", len(docIDs))
	}

	testIDs := map[string][]string{}
	testRe := regexp.MustCompile(`func (TestBudget(Rule|Edge)(\d+)_\w+)\(`)
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
			id := map[string]string{"Rule": "R", "Edge": "E"}[m[2]] + m[3]
			rel, _ := filepath.Rel(root, path)
			testIDs[id] = append(testIDs[id], m[1]+" ("+rel+")")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing, unknown []string
	for id := range docIDs {
		if len(testIDs[id]) == 0 {
			missing = append(missing, id)
		}
	}
	for id, tests := range testIDs {
		if !docIDs[id] {
			unknown = append(unknown, id+": "+strings.Join(tests, ", "))
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	if len(missing) > 0 {
		t.Errorf("rules/edge cases in docs/token-budgets.md with no test: %v", missing)
	}
	if len(unknown) > 0 {
		t.Errorf("tests naming IDs the doc does not define: %v", unknown)
	}
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
