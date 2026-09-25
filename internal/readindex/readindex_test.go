package readindex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func alwaysLive(Entry) bool { return true }

func TestLookupHitsTheSameUnchangedRead(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.go", "package a\n")
	x := New()
	x.Record(Key{Path: p}, "call_1", "result of call_1", true, "")
	e, ok := x.Lookup(Key{Path: p}, alwaysLive)
	if !ok || e.CallID != "call_1" {
		t.Fatalf("lookup = %+v, %v; want a hit on call_1", e, ok)
	}
}

func TestWholeReadAnswersAWindowButAWindowDoesNotAnswerWhole(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.go", "one\ntwo\n")
	x := New()
	x.Record(Key{Path: p}, "whole", "result of whole", true, "")
	if _, ok := x.Lookup(Key{Path: p, Offset: 2, Limit: 1}, alwaysLive); !ok {
		t.Fatal("a whole-file read must answer a windowed read of the same file")
	}

	y := New()
	y.Record(Key{Path: p, Offset: 1, Limit: 1}, "part", "result of part", false, "lines 1–1 of 2")
	if _, ok := y.Lookup(Key{Path: p}, alwaysLive); ok {
		t.Fatal("a windowed read must not answer a whole-file read")
	}
	if _, ok := y.Lookup(Key{Path: p, Offset: 2, Limit: 1}, alwaysLive); ok {
		t.Fatal("a different window is a different read")
	}
}

func TestPartialNoArgReadDoesNotAnswerWindows(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "big.go", "x\n")
	x := New()
	// A no-argument read that was truncated (Whole false) covers only itself.
	x.Record(Key{Path: p}, "first", "result of first", false, "lines 1–2000 of 5000")
	if _, ok := x.Lookup(Key{Path: p, Offset: 2001}, alwaysLive); ok {
		t.Fatal("a truncated read must not answer the next window")
	}
	if _, ok := x.Lookup(Key{Path: p}, alwaysLive); !ok {
		t.Fatal("the identical truncated read is still the same read")
	}
}

func TestStatChangeMisses(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.go", "v1\n")
	x := New()
	x.Record(Key{Path: p}, "c", "result of c", true, "")
	// A write from outside the harness (Bash, a formatter, the user).
	if err := os.WriteFile(p, []byte("version two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(p, future, future)
	if _, ok := x.Lookup(Key{Path: p}, alwaysLive); ok {
		t.Fatal("a file changed on disk must be read again")
	}
	if x.Len() != 0 {
		t.Fatal("the stale entry must be dropped")
	}
}

func TestInvalidateDropsHarnessMutations(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.go", "v1\n")
	x := New()
	x.Record(Key{Path: p}, "c1", "result of c1", true, "")
	x.Record(Key{Path: p, Offset: 5}, "c2", "result of c2", false, "")
	x.Invalidate(dir, "a.go") // relative, as the file-diff recorder reports
	if x.Len() != 0 {
		t.Fatalf("invalidate left %d entries", x.Len())
	}
}

func TestLookupMissesWhenTheResultLeftTheConversation(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.go", "v1\n")
	x := New()
	x.Record(Key{Path: p}, "cleared", "result of cleared", true, "")
	if _, ok := x.Lookup(Key{Path: p}, func(Entry) bool { return false }); ok {
		t.Fatal("a result cleared from context must be read from disk again")
	}
}

func TestBlobIDMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	p := writeFile(t, dir, "a.txt", "hello world\n")
	out, err := exec.Command("git", "hash-object", p).Output()
	if err != nil {
		t.Skip("git hash-object failed")
	}
	if got, want := blobID(p, 12), strings.TrimSpace(string(out)); got != want {
		t.Fatalf("blobID = %s, git hash-object = %s", got, want)
	}
}

func TestSummaryIsFactsOnly(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "secret.go", "API_KEY=do-not-leak\n")
	x := New()
	x.Record(Key{Path: p}, "c", "result of c", true, "")
	lines := x.Summary(dir, 10, nil)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "secret.go ") || !strings.Contains(lines[0], " blob ") {
		t.Fatalf("summary = %q", lines)
	}
	if strings.Contains(lines[0], "do-not-leak") {
		t.Fatal("the summary must never carry file contents")
	}
}

func TestNilIndexIsInert(t *testing.T) {
	var x *Index
	x.Record(Key{Path: "/nope"}, "c", "result of c", true, "")
	x.Invalidate("/", "a")
	if _, ok := x.Lookup(Key{Path: "/nope"}, alwaysLive); ok || x.Len() != 0 || x.Summary("/", 5, nil) != nil {
		t.Fatal("a nil index must do nothing")
	}
}
