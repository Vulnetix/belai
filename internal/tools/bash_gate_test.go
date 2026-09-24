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
		"git -c a=b log", "git --work-tree sub status",
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

func TestGitReadOnly(t *testing.T) {
	cases := []struct {
		fields []string
		want   bool
	}{
		{[]string{"git", "status"}, true},
		{[]string{"git", "log"}, true},
		{[]string{"git", "-C", "sub", "status"}, true},
		{[]string{"git", "-c", "a=b", "log"}, true},
		{[]string{"git", "--config-env", "foo", "status"}, true},
		{[]string{"git", "add", "."}, false},
		{[]string{"git", "commit", "-m", "x"}, false},
		{[]string{"git"}, false},
		{[]string{"git", "--version"}, false},
		{[]string{"git", "-C"}, false}, // dangling value option
	}
	for _, c := range cases {
		if got := gitReadOnly(c.fields); got != c.want {
			t.Errorf("gitReadOnly(%v) = %v, want %v", c.fields, got, c.want)
		}
	}
}

func TestEnvReadOnly(t *testing.T) {
	cases := []struct {
		fields []string
		want   bool
	}{
		{[]string{"env"}, true},
		{[]string{"env", "FOO=bar"}, true},
		{[]string{"env", "FOO=bar", "BAR=baz"}, true},
		{[]string{"env", "-i", "FOO=bar"}, true},
		{[]string{"env", "ls"}, false},
		{[]string{"env", "FOO=bar", "ls"}, false},
		{[]string{"env", "-u", "X"}, false},
		{[]string{"env", "--", "ls"}, false},
	}
	for _, c := range cases {
		if got := envReadOnly(c.fields); got != c.want {
			t.Errorf("envReadOnly(%v) = %v, want %v", c.fields, got, c.want)
		}
	}
}

func TestFindReadOnly(t *testing.T) {
	cases := []struct {
		fields []string
		want   bool
	}{
		{[]string{"find"}, true},
		{[]string{"find", ".", "-name", "x"}, true},
		{[]string{"find", ".", "-type", "f"}, true},
		{[]string{"find", ".", "-delete"}, false},
		{[]string{"find", ".", "-exec", "rm"}, false},
		{[]string{"find", ".", "-execdir", "ls"}, false},
		{[]string{"find", ".", "-ok", "cat"}, false},
		{[]string{"find", ".", "-okdir", "ls"}, false},
		{[]string{"find", ".", "-fprint", "/tmp/x"}, false},
		{[]string{"find", ".", "-fls", "/tmp/x"}, false},
		{[]string{"find", ".", "-fprintf", "/tmp/x"}, false},
	}
	for _, c := range cases {
		if got := findReadOnly(c.fields); got != c.want {
			t.Errorf("findReadOnly(%v) = %v, want %v", c.fields, got, c.want)
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
