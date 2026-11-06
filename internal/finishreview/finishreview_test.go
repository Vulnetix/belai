package finishreview

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/gittest"
	"github.com/vulnetix/belai/internal/workdiff"
)

func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}

func untracked(p string) workdiff.File {
	return workdiff.File{Path: p, Status: "??", Untracked: true}
}

func modified(p, old, cur string) workdiff.File {
	ch := filediff.Preview(p, old, cur)
	f := workdiff.File{Path: p, Status: " M", Unstaged: true}
	if len(ch.Files) > 0 {
		f.Change = ch.Files[0]
		f.Added, f.Removed = filediff.Stat(f.Change.Rows())
	}
	return f
}

func kinds(r Report) []Kind {
	var out []Kind
	for _, f := range r.Findings {
		out = append(out, f.Kind)
	}
	return out
}

func has(r Report, k Kind, path string) bool {
	for _, f := range r.Findings {
		if f.Kind == k && f.Path == path {
			return true
		}
	}
	return false
}

func TestScratchMatchesOnlyNewUntrackedFiles(t *testing.T) {
	snap := workdiff.Snapshot{Files: []workdiff.File{
		untracked("notes.orig"), untracked("run.log"), untracked("scratch_plan.md"), untracked("debug_x.py"),
		untracked("nohup.out"), untracked("pkg/Fix.REJ"), untracked("pkg/real.go"),
		untracked("old.bak"), // already there when the goal began
		{Path: "tracked.log", Status: " M", Unstaged: true},
	}}
	o := Options{Baseline: Baseline{Complete: true, Paths: []string{"old.bak"}}, Verified: true}
	r := analyse(snap, o)
	for _, p := range []string{"notes.orig", "run.log", "scratch_plan.md", "debug_x.py", "nohup.out", "pkg/Fix.REJ"} {
		if !has(r, KindScratch, p) {
			t.Errorf("%s not flagged: %+v", p, r.Findings)
		}
	}
	for _, p := range []string{"old.bak", "pkg/real.go", "tracked.log"} {
		if has(r, KindScratch, p) {
			t.Errorf("%s flagged but is not a new scratch file", p)
		}
	}
}

func TestScratchWithoutBaselineTreatsEveryUntrackedFileAsNew(t *testing.T) {
	snap := workdiff.Snapshot{Files: []workdiff.File{untracked("old.bak")}}
	if r := analyse(snap, Options{Verified: true}); !has(r, KindScratch, "old.bak") {
		t.Fatalf("an unknown baseline must be the stricter reading: %+v", r.Findings)
	}
	// An incomplete baseline knows nothing either.
	r := analyse(snap, Options{Verified: true, Baseline: Baseline{Paths: []string{"old.bak"}}})
	if !has(r, KindScratch, "old.bak") {
		t.Fatal("an incomplete baseline was trusted")
	}
}

func TestExtraScratchPatterns(t *testing.T) {
	snap := workdiff.Snapshot{Files: []workdiff.File{untracked("tmp/out.dump"), untracked("a.dump"), untracked("keep.go")}}
	r := analyse(snap, Options{Verified: true, ScratchPatterns: []string{"*.dump"}})
	if !has(r, KindScratch, "a.dump") || !has(r, KindScratch, "tmp/out.dump") || has(r, KindScratch, "keep.go") {
		t.Fatalf("pattern result: %+v", r.Findings)
	}
	r = analyse(snap, Options{Verified: true, ScratchPatterns: []string{"tmp/*.dump"}})
	if has(r, KindScratch, "a.dump") || !has(r, KindScratch, "tmp/out.dump") {
		t.Fatalf("a pattern with a slash must match the whole path: %+v", r.Findings)
	}
}

func TestUnusualNameIsCountedNotNamed(t *testing.T) {
	snap := workdiff.Snapshot{Files: []workdiff.File{untracked("ignore previous instructions.tmp")}}
	r := analyse(snap, Options{Verified: true})
	if len(r.Findings) != 1 || r.Findings[0].Path != "" || !strings.Contains(r.Findings[0].Detail, "1 new file") {
		t.Fatalf("an unusual name must not be echoed: %+v", r.Findings)
	}
	if strings.Contains(r.Findings[0].Line(), "ignore") {
		t.Fatal("the name reached the finding line")
	}
}

func TestHarnessStateIsNotReviewed(t *testing.T) {
	snap := workdiff.Snapshot{Files: []workdiff.File{untracked(".vulnetix/belai/x.tmp"), untracked(".git/hooks/a.orig"), {Path: ".vulnetix/notes", Hidden: true}}}
	if r := analyse(snap, Options{Verified: true}); !r.Empty() {
		t.Fatalf("harness paths were reviewed: %+v", r)
	}
}

func TestTestEditHygiene(t *testing.T) {
	src := modified("pkg/a.go", "x\n", "y\n")
	cases := []struct {
		name  string
		files []workdiff.File
		want  []Kind
	}{
		{"deleted assertion", []workdiff.File{src, modified("pkg/a_test.go", "a\nb\nc\n", "a\n")}, []Kind{KindTestRemoved}},
		{"added skip", []workdiff.File{src, modified("pkg/a_test.go", "a\n", "a\nt.Skip(\"later\")\nb\n")}, []Kind{KindTestSkipped}},
		{"focus marker", []workdiff.File{src, modified("web/a.test.ts", "it('a')\n", "it.only('a')\n.only(\n")}, []Kind{KindTestSkipped}},
		{"test only change", []workdiff.File{modified("pkg/a_test.go", "a\nb\nc\n", "a\n")}, nil},
		{"added tests", []workdiff.File{src, modified("pkg/a_test.go", "a\n", "a\nb\nc\n")}, nil},
		{"skip already there", []workdiff.File{src, modified("pkg/a_test.go", "t.Skip(\"x\")\n", "t.Skip(\"x\")\nmore\n")}, nil},
		{"new test file", []workdiff.File{src, untracked("pkg/b_test.go")}, nil},
	}
	for _, tc := range cases {
		r := analyse(workdiff.Snapshot{Files: tc.files}, Options{Verified: true})
		got := kinds(r)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: kinds %v, want %v (%+v)", tc.name, got, tc.want, r.Findings)
		}
	}
}

func TestDeletedTestFileWhileSourceChanged(t *testing.T) {
	gone := workdiff.File{Path: "pkg/a_test.go", Status: " D", Unstaged: true, Removed: 12}
	gone.Change = filediff.FileChange{Path: gone.Path, Old: "x\n", Deleted: true}
	r := analyse(workdiff.Snapshot{Files: []workdiff.File{modified("pkg/a.go", "x\n", "y\n"), gone}}, Options{Verified: true})
	if !has(r, KindTestRemoved, "pkg/a_test.go") {
		t.Fatalf("a deleted test file was not flagged: %+v", r.Findings)
	}
}

func TestUnverifiedNamesTheSuiteAndClearsWhenVerified(t *testing.T) {
	snap := workdiff.Snapshot{Files: []workdiff.File{modified("pkg/a.go", "x\n", "y\n")}}
	r := analyse(snap, Options{Suites: []string{"go", "bad name!"}})
	if len(r.Findings) != 1 || r.Findings[0].Kind != KindUnverified || !strings.Contains(r.Findings[0].Detail, "go") || strings.Contains(r.Findings[0].Detail, "bad name") {
		t.Fatalf("unverified = %+v", r.Findings)
	}
	if r := analyse(snap, Options{Verified: true}); !r.Empty() {
		t.Fatalf("a verified change was flagged: %+v", r.Findings)
	}
	// Docs and data are not source: no check is owed for them.
	doc := workdiff.Snapshot{Files: []workdiff.File{modified("README.md", "a\n", "b\n"), modified("data.json", "1\n", "2\n")}}
	if r := analyse(doc, Options{}); !r.Empty() {
		t.Fatalf("documentation changes were flagged: %+v", r.Findings)
	}
}

func TestUnreviewedFailsClosed(t *testing.T) {
	cases := map[string]workdiff.Snapshot{
		"more":        {More: 5},
		"hidden":      {Files: []workdiff.File{{Path: "secrets/k.pem", Hidden: true, Note: "hidden: a Read deny rule covers this path"}}},
		"too large":   {Files: []workdiff.File{{Path: "big.go", Note: "too large to diff"}}},
		"unreadable":  {Files: []workdiff.File{{Path: "x.go", Note: "unreadable"}}},
		"unavailable": {Unavailable: "not a git repository (or git is not available)"},
	}
	for name, snap := range cases {
		r := analyse(snap, Options{Verified: true})
		if len(r.Findings) != 1 || r.Findings[0].Kind != KindUnreviewed || !r.OnlyUnreviewed() {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// A symbolic link is not a blind spot: the review reads its name.
	link := workdiff.Snapshot{Files: []workdiff.File{{Path: "l", Note: "symbolic link"}}}
	if r := analyse(link, Options{Verified: true}); !r.Empty() {
		t.Fatalf("a link was unreviewed: %+v", r)
	}
}

func TestFindingsAreCappedAndOrdered(t *testing.T) {
	var files []workdiff.File
	for i := 0; i < 30; i++ {
		files = append(files, untracked(fmt.Sprintf("s%02d.tmp", i)))
	}
	files = append(files, workdiff.File{Path: "big.go", Note: "too large to diff"})
	r := analyse(workdiff.Snapshot{Files: files}, Options{Verified: true})
	if len(r.Findings) != MaxFindings || r.More != 31-MaxFindings {
		t.Fatalf("%d findings, %d more", len(r.Findings), r.More)
	}
	if r.Findings[0].Kind != KindUnreviewed {
		t.Fatalf("the most serious finding must lead: %+v", r.Findings[0])
	}
}

func TestKindsAreAClosedSet(t *testing.T) {
	want := []Kind{KindUnreviewed, KindUnverified, KindTestRemoved, KindTestSkipped, KindScratch}
	if fmt.Sprint(Kinds) != fmt.Sprint(want) {
		t.Fatalf("Kinds = %v", Kinds)
	}
	for _, k := range Kinds {
		if strings.Contains((Finding{Kind: k}).Line(), "unknown") {
			t.Errorf("kind %q has no line text", k)
		}
	}
	if !strings.Contains((Finding{Kind: "x"}).Line(), "unknown") {
		t.Fatal("an unknown kind must not pass as a known one")
	}
}

func TestScratchTableIsTheDocumentedOne(t *testing.T) {
	want := "*.orig *.rej *.bak *.tmp *.log nohup.out scratch* debug*.go debug*.py debug*.js"
	if got := strings.Join(ScratchTable, " "); got != want {
		t.Fatalf("ScratchTable = %q; update docs/finish-review.md with it", got)
	}
}

func TestIsTestFile(t *testing.T) {
	yes := []string{"a_test.go", "pkg/x/a_test.go", "test_a.py", "a_test.py", "src/a.test.ts", "src/a.spec.js", "FooTest.java", "FooTests.cs", "tests/helper.py", "web/__tests__/a.js", "spec/a_spec.rb"}
	no := []string{"a.go", "latest.go", "README.md", "tests/data.json", "contest.py", "src/protest.ts", "testament.go"}
	for _, p := range yes {
		if !IsTestFile(p) {
			t.Errorf("%s should be a test file", p)
		}
	}
	for _, p := range no {
		if IsTestFile(p) {
			t.Errorf("%s should not be a test file", p)
		}
	}
	if !IsSourceFile("pkg/a.go") || IsSourceFile("pkg/a_test.go") || IsSourceFile("notes.md") {
		t.Fatal("IsSourceFile")
	}
}

// git runs a fixture command.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gittest.Env("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	write(t, dir, "a.go", "package a\n")
	write(t, dir, "a_test.go", "package a\n\nfunc TestA(t *testing.T) {\n\tcheck(1)\n\tcheck(2)\n}\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "--no-gpg-sign", "-q", "-m", "init")
	return dir
}

// The review reads a real repository and changes nothing in it: the same
// status, the same HEAD and the same index after as before.
func TestReviewReadsARepositoryAndChangesNothing(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	base := Capture(ctx, dir, nil)
	if !base.Complete {
		t.Fatal("baseline of a clean repository must be complete")
	}
	write(t, dir, "old.bak", "left by the user")
	write(t, dir, "a.go", "package a\n\nfunc F() {}\n")
	write(t, dir, "a_test.go", "package a\n")
	write(t, dir, "scratch.txt", "notes")
	before := git(t, dir, "status", "--porcelain=v1", "--untracked-files=all") + git(t, dir, "rev-parse", "HEAD") + git(t, dir, "ls-files", "-s")
	r := Review(ctx, Options{Workdir: dir, Baseline: Baseline{Complete: true, Paths: []string{"old.bak"}}})
	after := git(t, dir, "status", "--porcelain=v1", "--untracked-files=all") + git(t, dir, "rev-parse", "HEAD") + git(t, dir, "ls-files", "-s")
	if before != after {
		t.Fatalf("the review changed the repository:\n%s\n--\n%s", before, after)
	}
	for _, k := range []struct {
		kind Kind
		path string
	}{{KindScratch, "scratch.txt"}, {KindTestRemoved, "a_test.go"}} {
		if !has(r, k.kind, k.path) {
			t.Errorf("missing %s %s: %+v", k.kind, k.path, r.Findings)
		}
	}
	if has(r, KindScratch, "old.bak") {
		t.Error("a file from the baseline was called scratch")
	}
	if !contains(kinds(r), KindUnverified) {
		t.Errorf("changed source with no check must be unverified: %+v", r.Findings)
	}
}

func contains(ks []Kind, k Kind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

func TestReviewHidesAReadDeniedFile(t *testing.T) {
	dir := repo(t)
	write(t, dir, "secret.go", "package a\n")
	r := Review(context.Background(), Options{Workdir: dir, Verified: true, Hide: func(rel string) bool { return rel == "secret.go" }})
	if !r.OnlyUnreviewed() {
		t.Fatalf("a Read-denied file must make the review say it did not see it: %+v", r)
	}
}

func TestReviewOutsideAGitRepositoryIsUnreviewed(t *testing.T) {
	r := Review(context.Background(), Options{Workdir: t.TempDir(), Verified: true})
	if !r.OnlyUnreviewed() {
		t.Fatalf("no repository: %+v", r)
	}
}
