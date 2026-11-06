package review

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/workdiff"
)

const sampleDiff = `diff --git a/internal/a.go b/internal/a.go
index 1111111..2222222 100644
--- a/internal/a.go
+++ b/internal/a.go
@@ -10,3 +10,4 @@ func f() {
 	x := 1
-	y := 2
+	y := 3
+	z := 4
 	return
@@ -40 +41,2 @@ func g() {
-	old()
+	one()
+	two()
diff --git a/doc/new file.md b/doc/new file.md
new file mode 100644
--- /dev/null
+++ b/doc/new file.md
@@ -0,0 +1,2 @@
+hello
+world
diff --git a/gone.go b/gone.go
deleted file mode 100644
index 3333333..0000000
--- a/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-a
-b
diff --git a/img.png b/img.png
index 4444444..5555555 100644
Binary files a/img.png and b/img.png differ
diff --git a/secret.txt b/secret.txt
--- a/secret.txt
+++ b/secret.txt
@@ -1 +1 @@
-a
+b
diff --git a/sub/ok.go b/sub/ok.go
--- a/sub/ok.go
+++ b/sub/ok.go
@@ -1,2 +1,3 @@
 package sub
+
 func ok() {}
`

func TestParseUnifiedReadsHunksAndSkips(t *testing.T) {
	d := ParseUnified(sampleDiff, Limits{MaxBytes: 1 << 20}, func(p string) bool { return p == "secret.txt" })
	want := map[string]string{
		"internal/a.go": "", "doc/new file.md": skipPath, "gone.go": skipDeleted,
		"img.png": skipBinary, "secret.txt": skipHidden, "sub/ok.go": "",
	}
	if len(d.Files) != len(want) {
		t.Fatalf("files = %+v", d.Files)
	}
	for _, f := range d.Files {
		if w, ok := want[f.Path]; !ok || f.Skip != w {
			t.Errorf("%s: skip %q, want %q", f.Path, f.Skip, w)
		}
	}
	if d.Reviewable() != 2 || d.Skipped() != 4 {
		t.Errorf("reviewable %d skipped %d", d.Reviewable(), d.Skipped())
	}
	a, _ := d.File("internal/a.go")
	if a.Added != 4 || a.Removed != 2 {
		t.Errorf("a.go +%d -%d, want +4 -2", a.Added, a.Removed)
	}
	for _, n := range []int{10, 12, 13, 41, 42} {
		if !a.HasLine(n) {
			t.Errorf("a.go line %d is in a hunk", n)
		}
	}
	for _, n := range []int{0, 9, 14, 40, 43, 1000} {
		if a.HasLine(n) {
			t.Errorf("a.go line %d is not in a hunk", n)
		}
	}
	if d.HasLine("secret.txt", 1) || d.HasLine("gone.go", 1) || d.HasLine("img.png", 1) || d.HasLine("nope.go", 1) {
		t.Error("a skipped or unknown file accepted a line")
	}
	if !d.HasLine("sub/ok.go", 3) || d.HasLine("sub/ok.go", 4) {
		t.Error("sub/ok.go hunk bounds are wrong")
	}
}

func TestParseUnifiedCapsBytesAndFiles(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString("diff --git a/f" + string(rune('a'+i)) + ".go b/f" + string(rune('a'+i)) + ".go\n--- a/f.go\n+++ b/f" + string(rune('a'+i)) + ".go\n@@ -1 +1 @@\n-x\n+" + strings.Repeat("y", 200) + "\n")
	}
	d := ParseUnified(b.String(), Limits{MaxBytes: 700}, nil)
	if !d.Truncated || d.Reviewable() >= 5 || d.Reviewable() == 0 {
		t.Fatalf("byte cap: truncated=%v reviewable=%d", d.Truncated, d.Reviewable())
	}
	capped := 0
	for _, f := range d.Files {
		if f.Skip == skipBytes {
			capped++
			if f.HasLine(1) {
				t.Error("a file past the cap still accepts lines")
			}
		}
	}
	if capped == 0 {
		t.Error("no file was marked past the byte cap")
	}
	d = ParseUnified(b.String(), Limits{MaxBytes: 1 << 20, MaxFiles: 2}, nil)
	if !d.Truncated || d.Reviewable() != 2 {
		t.Fatalf("file cap: truncated=%v reviewable=%d", d.Truncated, d.Reviewable())
	}
}

func TestParseUnifiedSurvivesWrongHunkCounts(t *testing.T) {
	text := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,50 +1,50 @@\n+x\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-a\n+b\n"
	d := ParseUnified(text, Limits{MaxBytes: 1 << 20}, nil)
	if _, ok := d.File("b.go"); !ok || !d.HasLine("b.go", 1) {
		t.Fatalf("the file after a hunk with wrong counts was lost: %+v", d.Files)
	}
	garbage := ParseUnified("@@ -a +b @@\nnot a diff\n\x00\xff", Limits{}, nil)
	if len(garbage.Files) != 0 {
		t.Fatalf("garbage produced files: %+v", garbage.Files)
	}
}

func TestParseUnifiedRenameAndOddPaths(t *testing.T) {
	text := "diff --git a/old.go b/new.go\nsimilarity index 90%\nrename from old.go\nrename to new.go\n--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/.git/config b/.git/config\n--- a/.git/config\n+++ b/.git/config\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/../etc/passwd b/../etc/passwd\n--- a/../etc/passwd\n+++ b/../etc/passwd\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git \"a/tab\\there\" \"b/tab\\there\"\n"
	d := ParseUnified(text, Limits{}, nil)
	if !d.HasLine("new.go", 1) {
		t.Errorf("rename lost: %+v", d.Files)
	}
	for _, p := range []string{".git/config", "../etc/passwd"} {
		if f, ok := d.File(p); ok && f.Reviewable() {
			t.Errorf("%s is reviewable", p)
		}
	}
	for _, f := range d.Files {
		if strings.Contains(f.Path, "tab") && f.Reviewable() {
			t.Errorf("a quoted path is reviewable: %+v", f)
		}
	}
}

func TestPlainPath(t *testing.T) {
	ok := []string{"a.go", "internal/x/y_z-1.go", "@scope/pkg+1/a", "a/b/.hidden"}
	bad := []string{"", "/abs", "a/../b", "a//b", ".git/x", "a b", "a\tb", "é.go", "a\x00", "a`b", "a:1", strings.Repeat("a", 401), "./a"}
	for _, p := range ok {
		if !PlainPath(p) {
			t.Errorf("%q refused", p)
		}
	}
	for _, p := range bad {
		if PlainPath(p) {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestFromSnapshot(t *testing.T) {
	mod := func(path, old, cur string) workdiff.File {
		ch := filediff.Preview(path, old, cur)
		fc := ch.Files[0]
		add, rem := filediff.Stat(fc.Rows())
		return workdiff.File{Path: path, Status: " M", Change: fc, Added: add, Removed: rem}
	}
	snap := workdiff.Snapshot{Files: []workdiff.File{
		mod("a.go", "one\ntwo\nthree\n", "one\nTWO\nthree\nfour\n"),
		{Path: "denied.go", Hidden: true, Note: "hidden"},
		{Path: "link", Note: "symbolic link"},
		{Path: "weird name.go", Change: filediff.FileChange{Path: "weird name.go", New: "x"}},
		{Path: "bin.dat", Change: filediff.FileChange{Binary: true}},
	}}
	d := FromSnapshot(snap, Limits{MaxBytes: 1 << 20})
	if !d.HasLine("a.go", 2) || !d.HasLine("a.go", 4) || d.HasLine("a.go", 9) {
		t.Fatalf("a.go spans: %+v", d.Files[0])
	}
	skips := map[string]string{"denied.go": skipHidden, "link": "symbolic link", "weird name.go": skipPath, "bin.dat": skipBinary}
	for _, f := range d.Files[1:] {
		if f.Skip != skips[f.Path] {
			t.Errorf("%s skip = %q, want %q", f.Path, f.Skip, skips[f.Path])
		}
	}
	snap.More = 3
	if !FromSnapshot(snap, Limits{}).Truncated {
		t.Error("files beyond the pane's cap did not mark the diff truncated")
	}
}
