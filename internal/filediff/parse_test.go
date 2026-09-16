package filediff

import (
	"sort"
	"strings"
	"testing"
)

func TestParseTargets(t *testing.T) {
	cases := []struct {
		name      string
		command   string
		want      []string
		confident bool
	}{
		// Redirections write whatever the program is.
		{"redirect", "echo hi > out.txt", []string{"out.txt"}, true},
		{"append", "echo hi >> out.txt", []string{"out.txt"}, true},
		{"glued redirect", "echo hi >out.txt", []string{"out.txt"}, true},
		{"stderr redirect", "echo hi 2> err.log", []string{"err.log"}, true},
		{"clobber", "echo hi >| out.txt", []string{"out.txt"}, true},
		{"both streams", "echo hi &> all.log", []string{"all.log"}, true},

		{"tee", "echo hi | tee out.txt", []string{"out.txt"}, true},
		{"tee append", "echo hi | tee -a out.txt", []string{"out.txt"}, true},

		{"sed in place", "sed -i 's/a/b/' main.go", []string{"main.go"}, true},
		{"sed in place suffix", "sed -i.bak 's/a/b/' main.go", []string{"main.go"}, true},
		{"sed to stdout", "sed 's/a/b/' main.go", nil, true},
		{"perl in place", "perl -pi -e 's/a/b/' main.go", []string{"main.go"}, true},

		{"mv", "mv old.go new.go", []string{"old.go", "new.go"}, true},
		{"cp", "cp src.go dst.go", []string{"dst.go"}, true},
		{"touch", "touch fresh.go", []string{"fresh.go"}, true},
		{"rm", "rm -f stale.go", []string{"stale.go"}, true},
		{"dd", "dd if=/dev/zero of=disk.img", []string{"disk.img"}, true},

		{"gofmt", "gofmt -w main.go", []string{"main.go"}, true},
		{"multiple files", "gofmt -w a.go b.go", []string{"a.go", "b.go"}, true},

		// Read-only commands change nothing.
		{"cat", "cat main.go", nil, true},
		{"grep", "grep -rn func .", nil, true},
		{"ls", "ls -la", nil, true},

		// Chained commands accumulate.
		{"chain", "echo a > one.txt && echo b > two.txt", []string{"one.txt", "two.txt"}, true},
		{"semicolons", "touch a.go ; touch b.go", []string{"a.go", "b.go"}, true},

		// Quoting keeps separators literal.
		{"quoted path", `echo hi > "my file.txt"`, []string{"my file.txt"}, true},
		{"quoted semicolon", `echo "a;b" > out.txt`, []string{"out.txt"}, true},

		// A heredoc body must not be parsed as shell.
		{"heredoc", "cat > out.txt <<EOF\nrm -rf /\nEOF", []string{"out.txt"}, true},
		{"quoted heredoc", "cat > out.txt <<'EOF'\n> decoy.txt\nEOF", []string{"out.txt"}, true},

		// Cases the rules cannot account for.
		{"unknown program", "make build", nil, false},
		{"go build", "go build ./...", nil, false},
		{"script", "./deploy.sh", nil, false},
		{"patch", "patch -p1 < fix.diff", nil, false},
		{"git apply", "git apply fix.diff", nil, false},
		{"variable target", "echo hi > $OUT", nil, false},
		{"substitution target", "echo hi > $(mktemp)", nil, false},
		{"backtick target", "echo hi > `mktemp`", nil, false},
		{"tilde target", "echo hi > ~/out.txt", nil, false},
		{"npm", "npm run build", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, confident := ParseTargets(tc.command)
			if confident != tc.confident {
				t.Fatalf("confident = %v, want %v (targets %v)", confident, tc.confident, got)
			}
			if !confident {
				return
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("targets = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestParseTargetsRefusesUnknownRatherThanGuessing is the property that keeps
// an inferred diff honest: an unrecognised command must never come back as
// "confident, nothing changed", because the UI would then show no diff for a
// command that changed everything.
func TestParseTargetsRefusesUnknownRatherThanGuessing(t *testing.T) {
	for _, cmd := range []string{
		"make", "cargo build", "python setup.py install", "docker build .",
		"go generate ./...", "npx tsc", "bash script.sh",
	} {
		if targets, confident := ParseTargets(cmd); confident {
			t.Fatalf("%q reported confident with targets %v", cmd, targets)
		}
	}
}
