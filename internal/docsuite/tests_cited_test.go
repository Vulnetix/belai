package docsuite

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// citedTest is a test function named in code formatting: `TestSomething`.
var citedTest = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")

// definedTest is a test function declaration in a _test.go file.
var definedTest = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)

// TestEveryTestTheDocsCiteExists keeps the claims the pages back with a test
// honest: a page that says "pinned by `TestX`" names a test that exists, so a
// renamed or deleted test cannot leave a rule that nothing holds.
func TestEveryTestTheDocsCiteExists(t *testing.T) {
	root := docparity.Root(t)
	defined := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "site", ".vulnetix", "vendor":
				return filepath.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range definedTest.FindAllStringSubmatch(string(data), -1) {
			defined[m[1]] = true
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(defined) < 500 {
		t.Fatalf("only %d tests found; the walk is wrong", len(defined))
	}

	pages := []string{"README.md", "AGENTS.md"}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			pages = append(pages, "docs/"+e.Name())
		}
	}
	cited := 0
	for _, page := range pages {
		for _, m := range citedTest.FindAllStringSubmatch(docparity.Read(t, page), -1) {
			cited++
			if !defined[m[1]] {
				t.Errorf("%s cites %s, which no _test.go file defines", page, m[1])
			}
		}
	}
	if cited < 150 {
		t.Fatalf("only %d test citations found; the pattern is wrong", cited)
	}
}
