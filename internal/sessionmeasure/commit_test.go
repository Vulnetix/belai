package sessionmeasure

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func TestParseLog(t *testing.T) {
	out := sha + "\x1fPix [bot]\x1fsecurity@vulnetix.com\x1fChris L\x1fchris@vulnetix.com\x1e" +
		"Signed-off-by: Pix [bot] <security@vulnetix.com>\n" +
		"Co-authored-by: Chris L <chris@vulnetix.com>\n" +
		"\n" +
		"Belai-Session: abc-123\n"
	got, err := ParseLog(out, "belai/k-0363e1")
	if err != nil {
		t.Fatal(err)
	}
	want := Commit{
		SHA: sha, AuthorName: "Pix [bot]", AuthorEmail: "security@vulnetix.com",
		CommitterName: "Chris L", CommitterEmail: "chris@vulnetix.com", Branch: "belai/k-0363e1",
		Trailers: []Trailer{
			{"Signed-off-by", "Pix [bot] <security@vulnetix.com>"},
			{"Co-authored-by", "Chris L <chris@vulnetix.com>"},
			{"Belai-Session", "abc-123"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLog = %+v\nwant %+v", got, want)
	}
}

func TestParseLogRefusesWhatIsNotACommit(t *testing.T) {
	for _, out := range []string{"", "fatal: bad revision", "nothex\x1fa\x1fb\x1fc\x1fd\x1e", sha + "\x1fa\x1fb\x1e"} {
		if _, err := ParseLog(out, ""); err == nil {
			t.Errorf("ParseLog(%q) accepted it", out)
		}
	}
}

func TestParseLogWithoutTrailers(t *testing.T) {
	got, err := ParseLog(sha+"\x1fA\x1fa@x.io\x1fA\x1fa@x.io\x1e\n", "")
	if err != nil || len(got.Trailers) != 0 || got.Branch != "" {
		t.Fatalf("ParseLog = %+v, %v", got, err)
	}
	if m := got.Meta(); m["trailers"] == nil {
		t.Fatalf("trailers must be an empty list, not null: %v", m)
	}
}

func TestParseLogCapsTrailers(t *testing.T) {
	var b strings.Builder
	b.WriteString(sha + "\x1fA\x1fa@x.io\x1fA\x1fa@x.io\x1e")
	for i := 0; i < maxTrailers+10; i++ {
		b.WriteString("Reviewed-by: someone\n")
	}
	got, err := ParseLog(b.String(), "")
	if err != nil || len(got.Trailers) != maxTrailers {
		t.Fatalf("trailers = %d, %v", len(got.Trailers), err)
	}
}

func TestCommitEntryShape(t *testing.T) {
	c := Commit{SHA: sha, AuthorName: "A", AuthorEmail: "a@x.io", CommitterName: "C", CommitterEmail: "c@x.io",
		Trailers: []Trailer{{"Signed-off-by", "A <a@x.io>"}}, Branch: "main"}
	e := c.Entry()
	if e.Type != "commit" || e.Role != "system" || e.Content != "" {
		t.Fatalf("entry = %+v", e)
	}
	want := map[string]any{
		"sha": sha, "author_name": "A", "author_email": "a@x.io", "committer_name": "C", "committer_email": "c@x.io",
		"trailers": []map[string]string{{"key": "Signed-off-by", "value": "A <a@x.io>"}}, "branch": "main",
	}
	if !reflect.DeepEqual(e.Meta, want) {
		t.Fatalf("meta = %v\nwant %v", e.Meta, want)
	}
}

// TestReadRealCommit reads a commit git made, with trailers and a committer who
// is not the author.
func TestReadRealCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(nil, "init", "-q", "-b", "work")
	run(nil, "config", "commit.gpgsign", "false")
	run([]string{"GIT_AUTHOR_NAME=Pix [bot]", "GIT_AUTHOR_EMAIL=security@vulnetix.com", "GIT_COMMITTER_NAME=Chris L", "GIT_COMMITTER_EMAIL=chris@vulnetix.com"},
		"commit", "-q", "--allow-empty", "-m", "feat: x\n\nbody text\n\nSigned-off-by: Pix [bot] <security@vulnetix.com>\nCo-authored-by: Chris L <chris@vulnetix.com>")

	git := func(ctx context.Context, args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
		return string(out), err
	}
	got, err := Read(context.Background(), git, "HEAD", "work")
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA != run(nil, "rev-parse", "HEAD") || got.AuthorName != "Pix [bot]" || got.AuthorEmail != "security@vulnetix.com" ||
		got.CommitterName != "Chris L" || got.CommitterEmail != "chris@vulnetix.com" || got.Branch != "work" {
		t.Fatalf("Read = %+v", got)
	}
	wantTrailers := []Trailer{{"Signed-off-by", "Pix [bot] <security@vulnetix.com>"}, {"Co-authored-by", "Chris L <chris@vulnetix.com>"}}
	if !reflect.DeepEqual(got.Trailers, wantTrailers) {
		t.Fatalf("trailers = %+v", got.Trailers)
	}
	if _, err := Read(context.Background(), git, "no-such-rev", ""); err == nil {
		t.Fatal("Read named a commit that does not exist")
	}
}

func TestReadHeadNamesTheBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	runner := func(ctx context.Context, d string, argv ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = d
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=A", "GIT_AUTHOR_EMAIL=a@x.io", "GIT_COMMITTER_NAME=A", "GIT_COMMITTER_EMAIL=a@x.io")
		return cmd.Output()
	}
	ctx := context.Background()
	for _, args := range [][]string{
		{"git", "init", "-q", "-b", "belai/k-1"},
		{"git", "config", "commit.gpgsign", "false"},
		{"git", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		if _, err := runner(ctx, dir, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	got, err := ReadHead(ctx, runner, dir)
	if err != nil || got.Branch != "belai/k-1" || len(got.SHA) != 40 || got.AuthorEmail != "a@x.io" {
		t.Fatalf("ReadHead = %+v, %v", got, err)
	}
	// A detached HEAD has no branch.
	if _, err := runner(ctx, dir, "git", "checkout", "-q", "--detach"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadHead(ctx, runner, dir); err != nil || got.Branch != "" {
		t.Fatalf("detached ReadHead = %+v, %v", got, err)
	}
	if _, err := ReadHead(ctx, runner, t.TempDir()); err == nil {
		t.Fatal("ReadHead read a commit from a directory that is not a repository")
	}
}
