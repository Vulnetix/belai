package shellsafe

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestSanitizationPageNamesEveryExportedFunction keeps docs/sanitization.md in
// step with the code.
func TestSanitizationPageNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, "docs/sanitization.md", docparity.ExportedFuncs(t, "."))
}

// TestSanitizationPageNamesTheReadOnlyPolicy pins the documented flag policy to
// the tables in code: every denied flag and wrapper is on the page.
func TestSanitizationPageNamesTheReadOnlyPolicy(t *testing.T) {
	doc := docparity.Read(t, "docs/sanitization.md")
	for name, d := range deniedFlags {
		if strings.Fields(d.long) == nil && d.short == "" {
			continue
		}
		if !strings.Contains(doc, "`"+name+"`") && !strings.Contains(doc, name) {
			t.Errorf("docs/sanitization.md does not mention the flag policy for %s", name)
		}
		for _, l := range strings.Fields(d.long) {
			if !strings.Contains(doc, l) {
				t.Errorf("docs/sanitization.md does not mention %s --%s", name, l)
			}
		}
	}
	for w := range simpleWrappers {
		if !strings.Contains(doc, w) {
			t.Errorf("docs/sanitization.md does not mention the wrapper %s", w)
		}
	}
	for _, g := range gitDeniedGlobal {
		if !strings.Contains(doc, "`"+g+"`") {
			t.Errorf("docs/sanitization.md does not mention git option %s", g)
		}
	}
	for f := range findUnsafe {
		if !strings.Contains(doc, "`"+f+"`") {
			t.Errorf("docs/sanitization.md does not mention find primary %s", f)
		}
	}
	for d := range trustedDirs {
		if !strings.Contains(doc, d) {
			t.Errorf("docs/sanitization.md does not mention the trusted directory %s", d)
		}
	}
}

// quote single-quotes s for a shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// wrapInShells puts cmd inside n layers of sh -c.
func wrapInShells(n int, cmd string) string {
	for i := 0; i < n; i++ {
		cmd = "sh -c " + quote(cmd)
	}
	return cmd
}

// TestNestingDepthIsThree pins "to a depth of three": three layers of sh -c are
// seen through, a fourth is opaque.
func TestNestingDepthIsThree(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sanitization.md")), " ")
	if !strings.Contains(doc, "to a depth of three") || maxDepth != 3 {
		t.Fatalf("the page says a depth of three; maxDepth is %d", maxDepth)
	}
	for layers := 1; layers <= 3; layers++ {
		a := Analyze(wrapInShells(layers, "git push"))
		if a.Has(FlagOpaque) {
			t.Errorf("%d layers of sh -c are opaque, the page says three are seen", layers)
		}
		found := false
		for _, s := range a.DenySubjects() {
			if s == "git push" {
				found = true
			}
		}
		if !found {
			t.Errorf("%d layers: a deny rule would not see git push in %v", layers, a.DenySubjects())
		}
	}
	if a := Analyze(wrapInShells(4, "git push")); !a.Has(FlagOpaque) {
		t.Error("four layers of sh -c were not marked opaque")
	}
}

// TestSourceLimitIs64KiB pins the size bound: a line of exactly 64 KiB parses,
// one byte more is not parseable.
func TestSourceLimitIs64KiB(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sanitization.md")), " ")
	if !strings.Contains(doc, "is over 64 KiB") || maxSource != 64<<10 {
		t.Fatalf("the page says 64 KiB; maxSource is %d", maxSource)
	}
	atLimit := "echo " + strings.Repeat("a", 64<<10-len("echo "))
	if err := Clean(atLimit); err != nil {
		t.Errorf("a line of exactly 64 KiB was refused: %v", err)
	}
	if a := Analyze(atLimit); !a.Parseable {
		t.Error("a line of exactly 64 KiB is not parseable")
	}
	if a := Analyze(atLimit + "a"); a.Parseable {
		t.Error("a line over 64 KiB is parseable")
	}
	if err := Clean(atLimit + "a"); err == nil {
		t.Error("Clean accepted a line over 64 KiB")
	}
}

// TestReadOnlyGitMatchesThePage pins the read subcommands, the refusals and the
// hardening the page describes.
func TestReadOnlyGitMatchesThePage(t *testing.T) {
	doc := docparity.Read(t, "docs/sanitization.md")
	for sub := range gitReadSubcommands {
		if !strings.Contains(doc, "`"+sub+"`") {
			t.Errorf("docs/sanitization.md does not list the read subcommand %s", sub)
		}
		if argv, reason := ReadOnly("git " + sub); argv == nil {
			t.Errorf("git %s is refused as read-only: %s", sub, reason)
		}
	}
	for _, line := range []string{"git push", "git commit", "git checkout main", "git -c core.pager=x log", "git --exec-path=/x log", "git log --output=/tmp/x", "git diff --ext-diff", "git show --textconv"} {
		if argv, _ := ReadOnly(line); argv != nil {
			t.Errorf("%q was accepted as read-only", line)
		}
	}
	argv, _ := ReadOnly("git diff")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-c core.fsmonitor=false", "-c core.pager=cat", "--no-ext-diff", "--no-textconv"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the argv that runs %q lacks %s; the page promises it is hardened", joined, want)
		}
	}
	// -C, --git-dir, --work-tree and --namespace are kept, as the page says.
	for _, line := range []string{"git -C /repo status", "git --git-dir=/r/.git status", "git --work-tree=/r status", "git --namespace=x status"} {
		if argv, reason := ReadOnly(line); argv == nil {
			t.Errorf("%q was refused: %s", line, reason)
		}
	}
}
