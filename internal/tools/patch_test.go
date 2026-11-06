package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/patchfmt"
)

// patchFixture is a root holding files, an ApplyPatch that has read every one
// of them, and the root.
func patchFixture(t *testing.T, files map[string]string) (*ApplyPatch, string) {
	t.Helper()
	root := t.TempDir()
	reads := NewReadState()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		reads.Note(p)
	}
	return &ApplyPatch{Root: root, Cwd: NewCwd(root), Reads: reads, Tolerance: patchfmt.Whitespace}, root
}

func patchArgs(body string) map[string]any {
	return map[string]any{"input": "*** Begin Patch\n" + body + "*** End Patch\n"}
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertUnchanged(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if got := readFile(t, root, name); got != body {
			t.Fatalf("%s changed: %q want %q", name, got, body)
		}
	}
}

func TestApplyPatchUpdatesAddsAndDeletesTogether(t *testing.T) {
	files := map[string]string{"a.txt": "one\ntwo\nthree\n", "b.txt": "gone\n", "dir/c.txt": "x\ny\n"}
	p, root := patchFixture(t, files)
	res, err := p.Execute(context.Background(), patchArgs(
		"*** Update File: a.txt\n one\n-two\n+TWO\n three\n"+
			"*** Update File: dir/c.txt\n x\n+between\n y\n"+
			"*** Add File: new/deep/n.txt\n+hello\n+world\n"+
			"*** Delete File: b.txt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, root, "a.txt"); got != "one\nTWO\nthree\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := readFile(t, root, "dir/c.txt"); got != "x\nbetween\ny\n" {
		t.Fatalf("c.txt = %q", got)
	}
	if got := readFile(t, root, "new/deep/n.txt"); got != "hello\nworld\n" {
		t.Fatalf("n.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt not deleted: %v", err)
	}
	if res.Kind != KindEdit || !strings.Contains(res.Content, "applied the patch to 4 files (2 updated, 1 added, 1 deleted)") {
		t.Fatalf("result = %+v", res)
	}
	abs, _ := res.Meta["abs_paths"].([]string)
	if len(abs) != 3 {
		t.Fatalf("abs_paths = %v, want the three files that still exist", abs)
	}
	// What the model wrote counts as read: a follow-up needs no re-read.
	if _, err := p.Execute(context.Background(), patchArgs("*** Update File: a.txt\n-TWO\n+two\n")); err != nil {
		t.Fatalf("follow-up patch: %v", err)
	}
}

func TestApplyPatchOneBadHunkChangesNothing(t *testing.T) {
	files := map[string]string{"a.txt": "one\ntwo\n", "b.txt": "x\ny\n"}
	p, root := patchFixture(t, files)
	_, err := p.Execute(context.Background(), patchArgs(
		"*** Update File: a.txt\n-one\n+ONE\n"+
			"*** Update File: b.txt\n-absent\n+z\n"+
			"*** Add File: c.txt\n+c\n"))
	if err == nil || !strings.Contains(err.Error(), "hunk 1 of b.txt") {
		t.Fatalf("err = %v", err)
	}
	assertUnchanged(t, root, files)
	if _, err := os.Stat(filepath.Join(root, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("c.txt was created by a failed patch")
	}
}

func TestApplyPatchAmbiguousHunkFails(t *testing.T) {
	files := map[string]string{"a.txt": "x\nx\n"}
	p, root := patchFixture(t, files)
	_, err := p.Execute(context.Background(), patchArgs("*** Update File: a.txt\n-x\n+y\n"))
	if err == nil || !strings.Contains(err.Error(), "match") {
		t.Fatalf("err = %v", err)
	}
	assertUnchanged(t, root, files)
}

func TestApplyPatchNeedsAReadFirst(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &ApplyPatch{Root: root, Cwd: NewCwd(root), Reads: NewReadState()}
	_, err := p.Execute(context.Background(), patchArgs("*** Update File: a.txt\n-one\n+two\n"))
	if err == nil || !strings.Contains(err.Error(), "has not been read") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, root, "a.txt") != "one\n" {
		t.Fatal("file changed")
	}
	// The approval preview does not demand the read.
	if got := p.PreviewFiles(patchArgs("*** Update File: a.txt\n-one\n+two\n")); len(got) != 1 || got[0].New != "two\n" {
		t.Fatalf("preview = %+v", got)
	}
}

func TestApplyPatchRefusesAChangedFile(t *testing.T) {
	p, root := patchFixture(t, map[string]string{"a.txt": "one\n"})
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := p.Execute(context.Background(), patchArgs("*** Update File: a.txt\n-one!\n+two\n"))
	if err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyPatchPathRefusals(t *testing.T) {
	p, root := patchFixture(t, map[string]string{"a.txt": "one\n"})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "o.txt"), []byte("o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "o.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"dotdot":         "*** Add File: ../escape.txt\n+x\n",
		"outside root":   "*** Update File: " + filepath.Join(outside, "o.txt") + "\n-o\n+p\n",
		"symlink update": "*** Update File: link.txt\n-o\n+p\n",
		"symlink delete": "*** Delete File: link.txt\n",
		"symlink dir":    "*** Add File: linkdir/new.txt\n+x\n",
		"add existing":   "*** Add File: a.txt\n+x\n",
		"update missing": "*** Update File: nope.txt\n-a\n+b\n",
		"delete missing": "*** Delete File: nope.txt\n",
		"directory":      "*** Delete File: .\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := p.Execute(context.Background(), patchArgs(body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if readFile(t, root, "a.txt") != "one\n" {
		t.Fatal("a.txt changed")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "o.txt")); string(b) != "o\n" {
		t.Fatalf("a file outside the root changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("a file was created through a symlinked directory")
	}
}

func TestApplyPatchRefusesBinaryAndOversize(t *testing.T) {
	files := map[string]string{"bin": "a\x00b\n", "big.txt": "line\n"}
	p, root := patchFixture(t, files)
	if _, err := p.Execute(context.Background(), patchArgs("*** Update File: bin\n-a\n+b\n")); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary: %v", err)
	}
	p.MaxBytes = 16
	if _, err := p.Execute(context.Background(), patchArgs("*** Add File: n.txt\n+"+strings.Repeat("x", 40)+"\n")); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("add oversize: %v", err)
	}
	if _, err := p.Execute(context.Background(), patchArgs("*** Update File: big.txt\n-line\n+"+strings.Repeat("y", 40)+"\n")); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("update oversize: %v", err)
	}
	assertUnchanged(t, root, files)
}

func TestApplyPatchToleranceFollowsTheSetting(t *testing.T) {
	files := map[string]string{"a.txt": "\tif x {\n\t\tdo()\n\t}\n"}
	body := "*** Update File: a.txt\n if x {\n \tdo()\n+\tmore()\n }\n"
	p, root := patchFixture(t, files)
	p.Tolerance = patchfmt.Strict
	if _, err := p.Execute(context.Background(), patchArgs(body)); err == nil {
		t.Fatal("strict accepted an indentation shift")
	}
	assertUnchanged(t, root, files)
	p.Tolerance = patchfmt.Whitespace
	res, err := p.Execute(context.Background(), patchArgs(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, root, "a.txt"); got != "\tif x {\n\t\tdo()\n\t\tmore()\n\t}\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if !strings.Contains(res.Content, "hunk 1 matched with indentation shifted") {
		t.Fatalf("result does not name the tier: %q", res.Content)
	}
}

func TestApplyPatchUnicodeIsOptIn(t *testing.T) {
	files := map[string]string{"a.txt": "say “hi”\n"}
	body := "*** Update File: a.txt\n-say \"hi\"\n+say \"bye\"\n"
	p, root := patchFixture(t, files)
	if _, err := p.Execute(context.Background(), patchArgs(body)); err == nil {
		t.Fatal("whitespace tolerance folded a curly quote")
	}
	assertUnchanged(t, root, files)
	p.Tolerance = patchfmt.Unicode
	if _, err := p.Execute(context.Background(), patchArgs(body)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, root, "a.txt"); got != "say \"bye\"\n" {
		t.Fatalf("a.txt = %q", got)
	}
}

func TestApplyPatchErrorsNeverQuoteFileText(t *testing.T) {
	const secret = "TOPSECRETVALUE"
	files := map[string]string{"a.txt": "token = " + secret + "\nother\n"}
	p, _ := patchFixture(t, files)
	_, err := p.Execute(context.Background(), patchArgs("*** Update File: a.txt\n-token = "+secret[:6]+"\n+x\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "token") {
		t.Fatalf("the error quoted file text: %v", err)
	}
}

func TestApplyPatchRollsBackWhenAWriteFails(t *testing.T) {
	files := map[string]string{"a.txt": "a\n", "b.txt": "b\n", "c.txt": "c\n", "gone.txt": "g\n"}
	p, root := patchFixture(t, files)
	calls := 0
	p.write = func(path string, content []byte) error {
		calls++
		if calls == 3 {
			return errors.New("disk full")
		}
		return writeFileAtomic(path, content)
	}
	_, err := p.Execute(context.Background(), patchArgs(
		"*** Update File: a.txt\n-a\n+A\n"+
			"*** Update File: b.txt\n-b\n+B\n"+
			"*** Update File: c.txt\n-c\n+C\n"+
			"*** Add File: made/n/new.txt\n+n\n"+
			"*** Delete File: gone.txt\n"))
	if err == nil || !strings.Contains(err.Error(), "every file was restored") {
		t.Fatalf("err = %v", err)
	}
	assertUnchanged(t, root, files)
	if _, err := os.Stat(filepath.Join(root, "made")); !os.IsNotExist(err) {
		t.Fatalf("a directory the patch made was left behind: %v", err)
	}
}

func TestApplyPatchRollsBackAFailedDelete(t *testing.T) {
	files := map[string]string{"a.txt": "a\n", "d1.txt": "1\n", "d2.txt": "2\n"}
	p, root := patchFixture(t, files)
	if err := os.Chmod(filepath.Join(root, "d1.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Remove a file's parent directory contents is not portable; fail the
	// delete by making the second target a directory after preparation.
	chs, err := p.prepare(mustParse(t, "*** Update File: a.txt\n-a\n+A\n*** Delete File: d1.txt\n*** Delete File: d2.txt\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "d2.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "d2.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d2.txt", "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.commit(chs); err == nil {
		t.Fatal("commit succeeded")
	}
	if got := readFile(t, root, "a.txt"); got != "a\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := readFile(t, root, "d1.txt"); got != "1\n" {
		t.Fatalf("d1.txt = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(root, "d1.txt")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode not restored: %v %v", fi, err)
	}
}

func mustParse(t *testing.T, body string) *patchfmt.Patch {
	t.Helper()
	pt, err := patchfmt.Parse("*** Begin Patch\n" + body + "*** End Patch\n")
	if err != nil {
		t.Fatal(err)
	}
	return pt
}

func TestApplyPatchPreviewMatchesTheWrite(t *testing.T) {
	files := map[string]string{"a.txt": "one\ntwo\n"}
	p, root := patchFixture(t, files)
	args := patchArgs("*** Update File: a.txt\n one\n-two\n+2\n*** Add File: n.txt\n+n\n")
	prev := p.PreviewFiles(args)
	if len(prev) != 2 || prev[0].Path != "a.txt" || prev[0].Old != "one\ntwo\n" || prev[0].New != "one\n2\n" || prev[1].Old != "" || prev[1].New != "n\n" {
		t.Fatalf("preview = %+v", prev)
	}
	assertUnchanged(t, root, files)
	if _, err := os.Stat(filepath.Join(root, "n.txt")); !os.IsNotExist(err) {
		t.Fatal("the preview wrote a file")
	}
	if _, err := p.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if readFile(t, root, "a.txt") != prev[0].New || readFile(t, root, "n.txt") != prev[1].New {
		t.Fatal("preview and write disagree")
	}
	if got := p.PreviewFiles(patchArgs("*** Update File: a.txt\n-nope\n+x\n")); got != nil {
		t.Fatalf("a patch that does not apply previewed %+v", got)
	}
}

func TestApplyPatchSubjectsTargetsAndKind(t *testing.T) {
	p, root := patchFixture(t, map[string]string{"a.txt": "one\n", "b.txt": "b\n"})
	args := patchArgs("*** Update File: a.txt\n-one\n+two\n*** Add File: sub/n.txt\n+n\n*** Delete File: b.txt\n")
	subs := p.PermissionSubjects(args)
	if len(subs) != 3 || subs[0].Tool != "Edit" || subs[1].Tool != "Write" || subs[2].Tool != "Edit" {
		t.Fatalf("subjects = %+v", subs)
	}
	if got := p.Targets(args); len(got) != 3 || got[0] != "a.txt" || got[1] != filepath.Join("sub", "n.txt") {
		t.Fatalf("targets = %v", got)
	}
	if p.Subject(args) != subs[0].Path {
		t.Fatalf("Subject = %q", p.Subject(args))
	}
	if !p.Mutates() || p.Kind() != KindEdit || p.Definition().Name != "apply_patch" {
		t.Fatal("tool identity wrong")
	}
	if p.PermissionSubjects(map[string]any{"input": "garbage"}) != nil || p.Targets(map[string]any{}) != nil {
		t.Fatal("an unparsable patch named files")
	}
	_ = root
}

func TestApplyPatchMissingInput(t *testing.T) {
	p, _ := patchFixture(t, nil)
	for _, args := range []map[string]any{{}, {"input": "  "}, {"input": "not a patch"}} {
		if _, err := p.Execute(context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestApplyPatchIsInTheDefaultRegistry(t *testing.T) {
	reg := Default(t.TempDir(), false)
	tl, ok := reg.Find(ApplyPatchName)
	if !ok {
		t.Fatal("apply_patch is not registered")
	}
	if _, ok := tl.(MultiSubject); !ok {
		t.Fatal("apply_patch does not implement MultiSubject")
	}
	if _, ok := tl.(MultiPreviewer); !ok {
		t.Fatal("apply_patch does not implement MultiPreviewer")
	}
	if _, ok := Default(t.TempDir(), true).Find(ApplyPatchName); ok {
		t.Fatal("read-only registry offers apply_patch")
	}
	// Its definition is long, so it is deferred behind ToolSearch and keeps the
	// core definitions inside their budget.
	if IsCoreTool(ApplyPatchName) {
		t.Fatal("apply_patch must stay a deferred tool")
	}
}
