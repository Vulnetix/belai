package shellsafe

import (
	"reflect"
	"strings"
	"testing"
)

func has(t *testing.T, subjects []string, want string) {
	t.Helper()
	for _, s := range subjects {
		if s == want {
			return
		}
	}
	t.Errorf("subjects %q lack %q", subjects, want)
}

func TestAnalyzeQuotesAreRemoved(t *testing.T) {
	cases := map[string]string{
		`git push`:                 "git push",
		`"git" push`:               "git push",
		`'git' 'push'`:             "git push",
		`g"i"t push`:               "git push",
		`git\ push`:                "git push",
		"git  \t push   origin":    "git push origin",
		`git push "origin main"`:   "git push origin main",
		`echo "a\"b" 'c\d'`:        `echo a"b c\d`,
		`git\` + "\n" + `push x`:   "gitpush x",
		`  git status  `:           "git status",
		`FOO=1 git push`:           "git push",
		`echo hi # a comment ; rm`: "echo hi",
	}
	for src, want := range cases {
		a := Analyze(src)
		if !a.Parseable || len(a.Commands) == 0 {
			t.Errorf("%q: unparseable or empty", src)
			continue
		}
		if got := a.Commands[0].Joined(); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

func TestAnalyzeFlags(t *testing.T) {
	cases := []struct {
		src  string
		want Flag
	}{
		{"ls", 0},
		{"ls -la", 0},
		{"ls; rm x", FlagChain},
		{"ls && rm x", FlagChain},
		{"ls || rm x", FlagChain},
		{"ls | wc", FlagChain},
		{"ls |& wc", FlagChain},
		{"ls\nrm x", FlagChain | FlagNewline},
		{"ls &", FlagBackground},
		{"(ls)", FlagSubshell},
		{"{ ls; }", FlagBlock},
		{"echo $(id)", FlagCmdSubst | FlagDynamic},
		{"echo `id`", FlagCmdSubst | FlagDynamic},
		{"cat <(ls)", FlagProcSubst | FlagDynamic},
		{"echo hi > f", FlagRedirect | FlagWriteRedirect},
		{"echo hi >> f", FlagRedirect | FlagWriteRedirect},
		{"echo hi &> f", FlagRedirect | FlagWriteRedirect},
		{"echo hi 2>&1", FlagRedirect},
		{"cat < f", FlagRedirect},
		{"cat <<EOF\nx\nEOF", FlagRedirect | FlagHeredoc | FlagNewline},
		{"cat <<< x", FlagRedirect | FlagHeredoc},
		{"echo $HOME", FlagExpansion | FlagDynamic},
		{"echo ${HOME}", FlagExpansion | FlagDynamic},
		{"echo $((1+1))", FlagExpansion | FlagDynamic},
		{"echo $'\\x41'", FlagExpansion | FlagDynamic},
		{"A=1 ls", FlagAssign},
		{"if true; then ls; fi", FlagControl},
		{"for i in 1 2; do ls; done", FlagControl},
		{"f() { ls; }", FlagControl | FlagBlock},
		{"! ls", FlagControl},
		{"ls *.go", FlagGlob},
	}
	for _, c := range cases {
		a := Analyze(c.src)
		if !a.Parseable {
			t.Errorf("%q: unparseable", c.src)
			continue
		}
		got := a.Flags
		if got&c.want != c.want {
			t.Errorf("%q: flags %b missing %b", c.src, got, c.want)
		}
		if c.want == 0 && got != 0 {
			t.Errorf("%q: unexpected flags %b", c.src, got)
		}
	}
}

func TestAnalyzeUnparseable(t *testing.T) {
	for _, src := range []string{"", "   ", "echo 'unterminated", "if then", "echo \x00x", "echo \x1bx", "a\xffb", strings.Repeat("a", maxSource+1)} {
		if a := Analyze(src); a.Parseable {
			t.Errorf("%q should not be parseable", src)
		}
	}
}

func TestWrappersAreUnwrapped(t *testing.T) {
	cases := []struct{ src, inner string }{
		{"env git push", "git push"},
		{"env FOO=1 BAR=2 git push", "git push"},
		{"env -i git push", "git push"},
		{"env -u FOO git push", "git push"},
		{"command git push", "git push"},
		{"exec git push", "git push"},
		{"nohup git push", "git push"},
		{"nice -n 5 git push", "git push"},
		{"sudo git push", "git push"},
		{"sudo -u root git push", "git push"},
		{"timeout 5 git push", "git push"},
		{"timeout -s KILL 5 git push", "git push"},
		{"xargs git push", "git push"},
		{"xargs -n 1 git push", "git push"},
		{"stdbuf -o0 git push", "git push"},
		{"sh -c 'git push'", "git push"},
		{"bash -c \"git push origin\"", "git push origin"},
		{"bash -lc 'git push'", "git push"},
		{"eval git push", "git push"},
		{"sh -c 'env git push'", "git push"},
		{"sh -c \"sh -c 'git push'\"", "git push"},
		{"find . -exec git push {} \\;", "git push {}"},
		{"find . -execdir git push {} +", "git push {}"},
		{"sh -c 'ls; git push'", "git push"},
		{"env sudo nohup git push", "git push"},
	}
	for _, c := range cases {
		a := Analyze(c.src)
		if !a.Parseable {
			t.Errorf("%q: unparseable", c.src)
			continue
		}
		has(t, a.Subjects(), c.inner)
	}
}

func TestOpaquePayloads(t *testing.T) {
	for _, src := range []string{
		"env -S 'git push'",
		"sh -c 'echo \"unterminated'",
		"sh -c \"sh -c \\\"sh -c \\\\\\\"sh -c ls\\\\\\\"\\\"\"",
	} {
		a := Analyze(src)
		if a.Parseable && !a.Has(FlagOpaque) {
			t.Errorf("%q should be opaque", src)
		}
	}
}

func TestDenySubjectsNormalise(t *testing.T) {
	cases := []struct{ src, want string }{
		{"git -c core.pager=x push", "git push"},
		{"git -C sub push origin", "git push origin"},
		{"git --no-pager push", "git push"},
		{"git --git-dir=x push", "git push"},
		{"git --git-dir x --work-tree y push", "git push"},
		{"/usr/bin/git push", "git push"},
		{"/usr/bin/git -c a=b push", "git push"},
		{"env /usr/local/bin/git push", "git push"},
		{"sh -c 'git -c a=b push'", "git push"},
	}
	for _, c := range cases {
		has(t, Analyze(c.src).DenySubjects(), c.want)
	}
	// An unparseable line still exposes its segments to deny rules.
	got := Analyze("git push; echo 'oops").DenySubjects()
	has(t, got, "git push")
}

func TestSimple(t *testing.T) {
	yes := []string{"ls", "git status", "cat -n file.go", "ls *.go", "echo 'a b'", "grep -rn foo ."}
	for _, s := range yes {
		if !Analyze(s).Simple() {
			t.Errorf("%q should be simple", s)
		}
	}
	no := []string{"", "ls; ls", "ls | ls", "echo $x", "echo $(x)", "ls > f", "A=1 ls", "ls &", "(ls)", "ls\nls", "if true; then ls; fi", "echo 'x", "ls <(ls)"}
	for _, s := range no {
		if Analyze(s).Simple() {
			t.Errorf("%q should not be simple", s)
		}
	}
}

func TestReadOnlyAllows(t *testing.T) {
	allow := []string{
		"cat x", "ls -la", "head -n 5 f", "grep foo f", "pwd", "wc -l f", "sort f", "uniq f", "file f", "jq . f",
		"echo hi", "env", "env FOO=bar", "env -i FOO=bar", "find . -name x", "find . -type f",
		"git status", "git log --oneline", "git diff", "git -C sub status", "git --work-tree sub status",
		"git rev-parse HEAD", "git ls-files", "git grep foo", "git describe --tags", "git show HEAD",
		"/usr/bin/git status", "/bin/cat x", "gunzip -c f.gz", "date", "date +%s", "date -u", "uniq f", "xxd f",
		"sort -u f", "sort -n -r f", "fd pattern", "rg -n foo", "tree -L 2", "du -sh .", "tail -n 20 f",
	}
	for _, s := range allow {
		if argv, why := ReadOnly(s); argv == nil {
			t.Errorf("ReadOnly(%q) refused: %s", s, why)
		}
	}
}

func TestReadOnlyRefuses(t *testing.T) {
	deny := []string{
		"", "   ", "rm -rf /", "touch x", "curl http://x", "echo a && echo b", "echo a | cat", "echo $(whoami)",
		"echo `whoami`", "echo a\nb", "ls > out", "ls >> out", "cat < f", "echo $HOME", "A=1 ls", "ls &", "(ls)",
		"git add .", "git commit -m x", "git", "git --version", "git push", "git -C", "git status; git push",
		"git -c core.pager=evil log", "git -c alias.x='!sh' x", "git --config-env=A=B status",
		"git --exec-path=/tmp status", "git -p log", "git --paginate log",
		"git diff --output=/tmp/x", "git log --output=x", "git show --output x", "git diff --ext-diff",
		"git grep -Ovim foo", "git grep --open-files-in-pager=vim foo",
		"env ls", "env FOO=bar ls", "env -u X", "env -S 'ls -l'", "env -- ls", "env -C /tmp",
		"find . -delete", "find . -exec rm {} ;", "find . -execdir ls {} +", "find . -ok cat {} ;", "find . -fprint /tmp/x",
		"sort -o out f", "sort --output=out f", "sort --out=out f", "sort -uo out f", "sort --compress-program=sh f",
		"shuf -o out f", "tree -o out", "rg --pre=sh x", "rg --pre sh x", "fd -x rm", "fd --exec rm", "fd -X rm",
		"date -s 12:00", "date --set=12:00", "date 010112002026", "file -C", "uniq in out", "xxd in out",
		"gunzip f.gz", "gunzip -k f.gz",
		"/tmp/x/cat f", "./cat f", "../bin/ls", "/home/u/bin/git status", "~/bin/ls", "/usr/bin/../../tmp/cat",
		"sh -c ls", "bash script.sh", "python -c x", "xargs ls", "sudo ls", "nohup ls",
	}
	for _, s := range deny {
		if argv, _ := ReadOnly(s); argv != nil {
			t.Errorf("ReadOnly(%q) = %q, want refusal", s, argv)
		}
	}
}

func TestReadOnlyGitArgvIsHardened(t *testing.T) {
	argv, why := ReadOnly("git -C sub log --oneline")
	if argv == nil {
		t.Fatal(why)
	}
	want := []string{"git", "-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-C", "sub", "log", "--no-ext-diff", "--no-textconv", "--oneline"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	argv, _ = ReadOnly("git status")
	want = []string{"git", "-c", "core.fsmonitor=false", "-c", "core.pager=cat", "status"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

func TestReadOnlyReturnsWhatWasAnalysed(t *testing.T) {
	// Quotes are removed exactly once, and the argv is what runs.
	argv, why := ReadOnly(`grep -n 'a b' "c d"`)
	if argv == nil {
		t.Fatal(why)
	}
	want := []string{"grep", "-n", "a b", "c d"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

func FuzzAnalyze(f *testing.F) {
	for _, s := range []string{
		"ls", "git push", "sh -c 'git push'", "env -S x", "echo $(id)", "a;b", "if x; then y; fi", "echo 'a", "\x00",
		"find . -exec sh -c 'rm {}' \\;", "cat <<EOF\nx\nEOF", "f() { :; }; f", "{ ls; }", "x=$(y) z",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a := Analyze(s)
		_ = a.Subjects()
		_ = a.DenySubjects()
		if argv, _ := ReadOnly(s); argv != nil {
			// Whatever ReadOnly accepts must be one plain command with no shell
			// syntax left, and its program must be allowlisted or git/find/env.
			b := Analyze(s)
			if !b.Simple() {
				t.Fatalf("ReadOnly accepted non-simple %q", s)
			}
			if b.Has(FlagChain | FlagCmdSubst | FlagRedirect | FlagBackground | FlagSubshell | FlagExpansion) {
				t.Fatalf("ReadOnly accepted %q with shell syntax", s)
			}
			if len(argv) == 0 {
				t.Fatalf("empty argv from %q", s)
			}
		}
	})
}
