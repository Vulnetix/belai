package finishreview

import (
	"path"
	"strings"
)

// codeExt is the set of extensions the review treats as source code. A file
// outside it (documentation, data, configuration) is not source and not a test.
var codeExt = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".mjs": true, ".cjs": true, ".java": true, ".kt": true, ".rb": true, ".rs": true,
	".cs": true, ".php": true, ".swift": true, ".scala": true, ".c": true, ".cc": true,
	".cpp": true, ".h": true, ".hpp": true,
}

// testDirs are directory names that hold tests.
var testDirs = map[string]bool{"test": true, "tests": true, "__tests__": true, "spec": true}

// IsSourceFile reports whether p is a source code file that is not a test.
func IsSourceFile(p string) bool {
	return codeExt[strings.ToLower(path.Ext(p))] && !IsTestFile(p)
}

// IsTestFile reports whether p is a test source file, by the conventional names
// of the common ecosystems: Go and Rust `_test`, Python `test_*` and `*_test`,
// JavaScript and TypeScript `.test.` and `.spec.`, Java, Kotlin and C# `*Test`
// and `*Tests`, Ruby `_spec` and `_test`, or any code file under a `test`,
// `tests`, `__tests__` or `spec` directory.
func IsTestFile(p string) bool {
	lower := strings.ToLower(p)
	ext := path.Ext(lower)
	if !codeExt[ext] {
		return false
	}
	name := strings.TrimSuffix(path.Base(p), path.Ext(p))
	lname := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lname, "_test"), strings.HasSuffix(lname, "_spec"),
		strings.HasPrefix(lname, "test_"),
		strings.HasSuffix(lname, ".test"), strings.HasSuffix(lname, ".spec"):
		return true
	case strings.HasSuffix(name, "Test"), strings.HasSuffix(name, "Tests"):
		return ext == ".java" || ext == ".kt" || ext == ".cs" || ext == ".scala" || ext == ".swift" || ext == ".php"
	}
	for _, seg := range strings.Split(path.Dir(lower), "/") {
		if testDirs[seg] {
			return true
		}
	}
	return false
}

// skipMarkers are the substrings that switch a test off or narrow a run to a
// few tests. They are counted in the file's old and new text; the text itself
// never leaves the package.
var skipMarkers = []string{
	"t.Skip(", "t.Skipf(", "t.SkipNow(", "b.Skip(",
	"xit(", "xdescribe(", "xtest(", "it.skip(", "describe.skip(", "test.skip(",
	".only(", "@Disabled", "@Ignore", "@pytest.mark.skip", "pytest.skip(",
	"@unittest.skip", "#[ignore]", "[Ignore]", "[Fact(Skip",
}

// countSkips counts the markers in text.
func countSkips(text string) int {
	n := 0
	for _, m := range skipMarkers {
		n += strings.Count(text, m)
	}
	return n
}
