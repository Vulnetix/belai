package tools

import "testing"

// TestBashAllowed is the single gate every read-only Bash command passes
// through. It pins the allowlist, the metacharacter rejection, the
// basename-normalised dispatch (a full path to git still routes to the git
// branch), and the fail-closed subcommand checks.
func TestBashAllowed(t *testing.T) {
	allow := []string{
		"cat x", "ls -la", "head -n 5 f", "grep foo f", "pwd",
		"env", "env FOO=bar", "env FOO=bar BAR=baz",
		"git status", "git log --oneline", "git diff", "git -C sub status",
		"git --work-tree sub status",
		"/usr/bin/git status", // basename dispatch
		"find . -name x", "find . -type f",
		"echo hi", "wc -l f", "sort f", "uniq f", "file f", "jq . f",
	}
	for _, cmd := range allow {
		if !BashAllowed(cmd) {
			t.Errorf("BashAllowed(%q) = false, want true", cmd)
		}
	}

	deny := []string{
		"",
		"   ",
		"rm -rf /",
		"echo a && echo b",
		"echo a | cat",
		"echo $(whoami)",
		"echo `whoami`",
		"echo a\nb",
		"touch x",
		"git add .",
		"git commit -m x",
		"git -c a=b log", // -c can name a program to run
		"git --config-env=A=B status",
		"git --exec-path=/tmp status",
		"git diff --output=/tmp/x",
		"sort -o out f",
		"rg --pre=sh x",
		"/tmp/x/cat f",   // not an allowlisted directory
		"git",            // no subcommand
		"git --version",  // option, never a subcommand
		"env ls",         // bare token executes
		"env FOO=bar ls", // assignment then a bare token
		"env -u X",       // -u's argument is a bare token: fail closed
		"find . -delete",
		"find . -exec rm",
		"find . -ok cat",
		"curl http://x",
	}
	for _, cmd := range deny {
		if BashAllowed(cmd) {
			t.Errorf("BashAllowed(%q) = true, want false", cmd)
		}
	}
}

func TestBashKindAndMutates(t *testing.T) {
	if got := (&Bash{}).Kind(); got != KindBash {
		t.Fatalf("Kind = %q, want %q", got, KindBash)
	}
	if !(&Bash{}).Mutates() {
		t.Fatal("full-mode Bash must report it mutates")
	}
	if (&Bash{ReadOnly: true}).Mutates() {
		t.Fatal("read-only Bash must not report it mutates")
	}
}
