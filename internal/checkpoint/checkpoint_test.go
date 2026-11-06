package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/permissions"
)

// fixture is a working directory with a tracker whose policy confines restores
// to it.
type fixture struct {
	t    *testing.T
	root string
	tr   *Tracker
}

func newFixture(t *testing.T, lim Limits) *fixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := config.CheckpointsDir(root, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if lim.MaxPerSession == 0 {
		lim.MaxPerSession = 50
	}
	if lim.MaxBytes == 0 {
		lim.MaxBytes = 200 << 20
	}
	tr, err := Open(dir, lim, PolicyFor(root, func(abs string) bool {
		return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
	}, permissions.Settings{}))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, root: root, tr: tr}
}

func (f *fixture) path(name string) string { return filepath.Join(f.root, name) }

func (f *fixture) write(name, body string) string {
	f.t.Helper()
	p := f.path(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) read(name string) string {
	f.t.Helper()
	b, err := os.ReadFile(f.path(name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// restoreLatest plans and applies the latest turn checkpoint.
func (f *fixture) restoreLatest() (Plan, Result) {
	f.t.Helper()
	info, ok := f.tr.Latest()
	if !ok {
		f.t.Fatal("no checkpoint to restore")
	}
	plan, err := f.tr.Plan(info.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	res, err := f.tr.Apply(plan)
	if err != nil {
		f.t.Fatal(err)
	}
	return plan, res
}

func TestRoundTripRestoresAndDeletes(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "one\n")
	if err := os.Chmod(a, 0o755); err != nil {
		t.Fatal(err)
	}
	created := f.path("new.txt")

	f.tr.BeginTurn("entry1", "change a and add new")
	if err := f.tr.Save(a); err != nil {
		t.Fatal(err)
	}
	if err := f.tr.Save(created); err != nil {
		t.Fatal(err)
	}
	f.write("a.txt", "two\n")
	f.write("new.txt", "fresh\n")

	_, res := f.restoreLatest()
	if res.Restored != 1 || res.Deleted != 1 || res.Safety == "" {
		t.Fatalf("result = %+v", res)
	}
	if got := f.read("a.txt"); got != "one\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if fi, _ := os.Stat(a); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", fi.Mode().Perm())
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the created file survived: %v", err)
	}
}

func TestOnlyTheFirstTouchOfATurnIsKept(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "original")
	f.tr.BeginTurn("", "p")
	if err := f.tr.Save(a); err != nil {
		t.Fatal(err)
	}
	f.write("a.txt", "mid")
	if err := f.tr.Save(a); err != nil {
		t.Fatal(err)
	}
	f.write("a.txt", "final")
	list := f.tr.List()
	if len(list) != 1 || list[0].Files != 1 {
		t.Fatalf("list = %+v", list)
	}
	f.restoreLatest()
	if got := f.read("a.txt"); got != "original" {
		t.Fatalf("a.txt = %q, want the state before the turn", got)
	}
}

func TestATurnThatTouchesNothingLeavesNoCheckpoint(t *testing.T) {
	f := newFixture(t, Limits{})
	f.tr.BeginTurn("e", "just reading")
	f.tr.Seal()
	if len(f.tr.List()) != 0 {
		t.Fatal("an empty turn made a checkpoint")
	}
	if _, ok := f.tr.Latest(); ok {
		t.Fatal("Latest found a checkpoint")
	}
}

func TestRewindRestoresEveryTurnSinceAndUndoWalksBack(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "v0")
	b := f.write("b.txt", "b0")

	f.tr.BeginTurn("e1", "turn one")
	_ = f.tr.Save(a)
	f.write("a.txt", "v1")
	f.tr.BeginTurn("e2", "turn two")
	_ = f.tr.Save(a)
	_ = f.tr.Save(b)
	f.write("a.txt", "v2")
	f.write("b.txt", "b2")

	// /undo undoes turn two only.
	f.restoreLatest()
	if f.read("a.txt") != "v1" || f.read("b.txt") != "b0" {
		t.Fatalf("after one undo: a=%q b=%q", f.read("a.txt"), f.read("b.txt"))
	}
	// A second /undo goes one prompt further back instead of repeating.
	f.restoreLatest()
	if f.read("a.txt") != "v0" {
		t.Fatalf("after two undos: a=%q", f.read("a.txt"))
	}
	if _, ok := f.tr.Latest(); ok {
		t.Fatal("both turn checkpoints are undone, Latest should be empty")
	}
}

func TestRestoreIsItselfUndoableThroughTheSafetyCheckpoint(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "before")
	f.tr.BeginTurn("e", "p")
	_ = f.tr.Save(a)
	f.write("a.txt", "after")

	_, res := f.restoreLatest()
	if f.read("a.txt") != "before" {
		t.Fatal("restore did not happen")
	}
	// The user can restore the safety checkpoint to get "after" back.
	plan, err := f.tr.Plan(res.Safety)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.tr.Apply(plan); err != nil {
		t.Fatal(err)
	}
	if got := f.read("a.txt"); got != "after" {
		t.Fatalf("a.txt = %q, want the overwritten content back", got)
	}
}

func TestPlanLeavesFilesAlreadyInPlaceOut(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "same")
	f.tr.BeginTurn("", "p")
	_ = f.tr.Save(a) // the turn then changed nothing
	info, _ := f.tr.Latest()
	plan, err := f.tr.Plan(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("plan = %+v, want nothing to do", plan.Changes)
	}
}

func TestBinaryCredentialAndLargeFilesAreNamedNotCopied(t *testing.T) {
	f := newFixture(t, Limits{})
	bin := f.write("data.dat", "a\x00b")
	env := f.write(".env", "TOKEN=abc")
	key := f.write("id_rsa", "-----BEGIN-----")
	big := f.path("big.txt")
	if err := os.WriteFile(big, make([]byte, MaxFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	f.tr.BeginTurn("", "p")
	for _, p := range []string{bin, env, key, big} {
		if err := f.tr.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	list := f.tr.List()
	if list[0].Files != 4 || list[0].Restorable != 0 {
		t.Fatalf("info = %+v, want 4 files and none restorable", list[0])
	}
	if blobs, _ := os.ReadDir(filepath.Join(f.tr.Dir(), "blobs")); len(blobs) != 0 {
		t.Fatalf("%d blobs stored for files that must not be copied", len(blobs))
	}
	f.write(".env", "TOKEN=changed")
	info, _ := f.tr.Latest()
	plan, _ := f.tr.Plan(info.ID)
	for _, c := range plan.Changes {
		if c.Action != ActSkip || !strings.Contains(c.Why, "not restorable") {
			t.Fatalf("change = %+v, want a skip", c)
		}
	}
	if f.read(".env") != "TOKEN=changed" {
		t.Fatal("a skipped file was touched")
	}
}

func TestReadDenyRuleKeepsContentOutOfTheStore(t *testing.T) {
	f := newFixture(t, Limits{})
	perms := permissions.From(nil, nil, []string{"Read(secret/*)"})
	f.tr.SetPolicy(PolicyFor(f.root, func(string) bool { return true }, perms))
	p := f.write("secret/x.txt", "classified")
	f.tr.BeginTurn("", "p")
	if err := f.tr.Save(p); err != nil {
		t.Fatal(err)
	}
	if blobs, _ := os.ReadDir(filepath.Join(f.tr.Dir(), "blobs")); len(blobs) != 0 {
		t.Fatal("a file a Read rule withholds was copied")
	}
	if l := f.tr.List(); l[0].Restorable != 0 {
		t.Fatalf("info = %+v", l[0])
	}
}

func TestRestoreRefusesPathsOutsideRootsSymlinksAndWriteDenyRules(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "orig")
	denied := f.write("locked/l.txt", "orig")
	linked := f.write("real.txt", "orig")
	f.tr.BeginTurn("", "p")
	for _, p := range []string{a, denied, linked} {
		_ = f.tr.Save(p)
	}
	f.write("a.txt", "changed")
	f.write("locked/l.txt", "changed")
	f.write("real.txt", "changed")

	// The path becomes a symbolic link to a file outside the roots.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.path("real.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, f.path("real.txt")); err != nil {
		t.Skip("symlinks unavailable")
	}

	perms := permissions.From(nil, nil, []string{"Write(locked/*)"})
	f.tr.SetPolicy(PolicyFor(f.root, func(abs string) bool {
		return abs == f.root || strings.HasPrefix(abs, f.root+string(filepath.Separator))
	}, perms))

	_, res := f.restoreLatest()
	if res.Restored != 1 || res.Skipped != 2 {
		t.Fatalf("result = %+v, want a restored and two skipped", res)
	}
	if f.read("a.txt") != "orig" {
		t.Fatal("the plain file was not restored")
	}
	if f.read("locked/l.txt") != "changed" {
		t.Fatal("a Write deny rule did not stop the restore")
	}
	if b, _ := os.ReadFile(outside); string(b) != "outside" {
		t.Fatalf("the restore wrote through a link: %q", b)
	}
}

func TestRestoreRefusesAPathResolvingOutsideTheRoots(t *testing.T) {
	f := newFixture(t, Limits{})
	outsideDir := t.TempDir()
	// A directory inside the root is replaced by a link to elsewhere.
	p := f.write("sub/f.txt", "orig")
	f.tr.BeginTurn("", "p")
	_ = f.tr.Save(p)
	if err := os.RemoveAll(f.path("sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, f.path("sub")); err != nil {
		t.Skip("symlinks unavailable")
	}
	_, res := f.restoreLatest()
	if res.Restored != 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "f.txt")); err == nil {
		t.Fatal("the restore wrote outside the roots")
	}
}

func TestCountLimitDropsTheOldest(t *testing.T) {
	f := newFixture(t, Limits{MaxPerSession: 3})
	a := f.write("a.txt", "x")
	for i := 0; i < 6; i++ {
		f.tr.BeginTurn("", "t")
		_ = f.tr.Save(a)
	}
	list := f.tr.List()
	if len(list) != 3 || list[0].Seq != 4 {
		t.Fatalf("list = %+v, want the newest 3", list)
	}
	// The index on disk was rewritten, so a reopen sees the same.
	again, err := Open(f.tr.Dir(), Limits{MaxPerSession: 3, MaxBytes: 1 << 30}, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.List()) != 3 {
		t.Fatalf("reopened list = %d", len(again.List()))
	}
}

func TestByteLimitDropsTheOldestAndNeverTheOpenCheckpoint(t *testing.T) {
	f := newFixture(t, Limits{MaxBytes: 100})
	f.tr.BeginTurn("", "one")
	_ = f.tr.Save(f.write("a.txt", strings.Repeat("a", 60)))
	f.tr.BeginTurn("", "two")
	_ = f.tr.Save(f.write("b.txt", strings.Repeat("b", 60)))
	list := f.tr.List()
	if len(list) != 1 || list[0].Label != "two" {
		t.Fatalf("list = %+v, want only the newest checkpoint", list)
	}
	// One file larger than the whole budget is named, not copied.
	f.tr.BeginTurn("", "three")
	_ = f.tr.Save(f.write("c.txt", strings.Repeat("c", 150)))
	l := f.tr.List()
	last := l[len(l)-1]
	if last.Label != "three" || last.Restorable != 0 {
		t.Fatalf("last = %+v", last)
	}
	var used int64
	ents, _ := os.ReadDir(filepath.Join(f.tr.Dir(), "blobs"))
	for _, e := range ents {
		fi, _ := e.Info()
		used += fi.Size()
	}
	if used > 100 {
		t.Fatalf("%d bytes of blobs exceed the limit", used)
	}
}

func TestStoreIsPrivate(t *testing.T) {
	f := newFixture(t, Limits{})
	f.tr.BeginTurn("", "p")
	_ = f.tr.Save(f.write("a.txt", "secret-ish"))
	for _, p := range []string{f.tr.Dir(), filepath.Join(f.tr.Dir(), "blobs")} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %v, %v", p, fi.Mode().Perm(), err)
		}
	}
	for _, p := range []string{filepath.Join(f.tr.Dir(), "index.jsonl")} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v", p, fi.Mode().Perm(), err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(f.tr.Dir(), "blobs"))
	for _, e := range ents {
		if fi, _ := e.Info(); fi.Mode().Perm() != 0o600 {
			t.Fatalf("blob mode = %v", fi.Mode().Perm())
		}
	}
	if !config.UnderCheckpoints(filepath.Join(f.tr.Dir(), "blobs", "x")) {
		t.Fatal("the store is not recognised as the checkpoint store")
	}
}

func TestSaveRefusesTheStoreItself(t *testing.T) {
	f := newFixture(t, Limits{})
	f.tr.BeginTurn("", "p")
	if err := f.tr.Save(filepath.Join(f.tr.Dir(), "index.jsonl")); err == nil {
		t.Fatal("the store was offered as a file to keep")
	}
}

func TestUnavailableTrackerFailsClosed(t *testing.T) {
	tr := Unavailable(errors.New("disk full"))
	tr.BeginTurn("", "p")
	if err := tr.Save("/tmp/x"); err == nil {
		t.Fatal("Save succeeded")
	}
	if err := tr.Ready(); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Ready = %v", err)
	}
	if _, err := tr.Apply(Plan{Changes: []Change{{Path: "/tmp/x", Action: ActDelete}}}); err == nil {
		t.Fatal("Apply succeeded on a failed store")
	}
}

func TestReadyFailsWhenTheStoreCannotBeWritten(t *testing.T) {
	f := newFixture(t, Limits{})
	if err := f.tr.Ready(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.tr.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := f.tr.Ready(); err == nil {
		t.Fatal("Ready passed with the store directory gone")
	}
	f.tr.BeginTurn("", "p")
	if err := f.tr.Save(f.write("a.txt", "x")); err == nil {
		t.Fatal("Save succeeded after Ready failed")
	}
}

func TestSafetyCopyFailureStopsBeforeAnyWrite(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "orig")
	f.tr.BeginTurn("", "p")
	_ = f.tr.Save(a)
	f.write("a.txt", "changed")
	info, _ := f.tr.Latest()
	plan, _ := f.tr.Plan(info.ID)
	if err := os.RemoveAll(f.tr.Dir()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tr.Apply(plan); err == nil {
		t.Fatal("Apply went ahead without a safety copy")
	}
	if f.read("a.txt") != "changed" {
		t.Fatal("a file was written although the safety copy failed")
	}
}

func TestBaselinesRecordShellEffects(t *testing.T) {
	f := newFixture(t, Limits{})
	edited := f.write("edited.txt", "e0")
	removed := f.write("removed.txt", "r0")
	created := f.path("created.txt")
	big := f.path("big.dat")
	f.write("big.dat", "x")

	f.tr.BeginTurn("", "run a build")
	err := f.tr.SaveBaselines([]Baseline{
		{Path: edited, Existed: true, Known: true, Body: []byte("e0"), Mode: 0o644},
		{Path: removed, Existed: true, Known: true, Body: []byte("r0"), Mode: 0o600},
		{Path: created},
		{Path: big, Existed: true, Why: WhyLarge},
	}, "one command's effects could not be seen")
	if err != nil {
		t.Fatal(err)
	}
	f.write("edited.txt", "e1")
	_ = os.Remove(removed)
	f.write("created.txt", "c1")

	l := f.tr.List()
	if l[0].Files != 4 || l[0].Gaps != 1 || l[0].Restorable != 3 {
		t.Fatalf("info = %+v", l[0])
	}
	plan, res := f.restoreLatest()
	if f.read("edited.txt") != "e0" || f.read("removed.txt") != "r0" {
		t.Fatal("the shell command's changes were not restored")
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the created file survived")
	}
	if fi, _ := os.Stat(removed); fi.Mode().Perm() != 0o600 {
		t.Fatalf("the removed file came back with mode %v", fi.Mode().Perm())
	}
	if res.Restored != 2 || res.Deleted != 1 || plan.Gaps != 1 {
		t.Fatalf("res = %+v gaps = %d", res, plan.Gaps)
	}
}

func TestGapWithNoCheckpointWaitsForTheFirstRecordedFile(t *testing.T) {
	f := newFixture(t, Limits{})
	f.tr.BeginTurn("", "p")
	if err := f.tr.SaveBaselines(nil, "an unseen effect"); err != nil {
		t.Fatal(err)
	}
	if len(f.tr.List()) != 0 {
		t.Fatal("a gap alone made a checkpoint")
	}
	_ = f.tr.Save(f.write("a.txt", "x"))
	if l := f.tr.List(); len(l) != 1 || l[0].Gaps != 1 {
		t.Fatalf("list = %+v, want the waiting note attached", l)
	}
	// A note never carries over a turn boundary.
	f.tr.BeginTurn("", "q")
	_ = f.tr.SaveBaselines(nil, "second")
	f.tr.BeginTurn("", "r")
	_ = f.tr.Save(f.write("b.txt", "y"))
	l := f.tr.List()
	if l[len(l)-1].Gaps != 0 {
		t.Fatalf("a gap leaked into the next turn: %+v", l[len(l)-1])
	}
}

func TestReopenKeepsTheRecordsAndSkipsBadLines(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "keep me")
	f.tr.BeginTurn("entry-9", "fix the parser")
	_ = f.tr.Save(a)
	f.write("a.txt", "changed")

	idx := filepath.Join(f.tr.Dir(), "index.jsonl")
	fh, _ := os.OpenFile(idx, os.O_APPEND|os.O_WRONLY, 0o600)
	// A torn line, a record with a relative path, and one with a bad hash.
	_, _ = fh.WriteString("{not json\n")
	_, _ = fh.WriteString(`{"t":"f","cp":"c1","path":"relative/x","state":"file","sha":"` + strings.Repeat("a", 64) + `"}` + "\n")
	_, _ = fh.WriteString(`{"t":"f","cp":"c1","path":"/abs/y","state":"file","sha":"zz"}` + "\n")
	_ = fh.Close()

	again, err := Open(f.tr.Dir(), Limits{MaxPerSession: 50, MaxBytes: 1 << 30}, f.tr.pol)
	if err != nil {
		t.Fatal(err)
	}
	l := again.List()
	if len(l) != 1 || l[0].Files != 1 || l[0].Entry != "entry-9" || l[0].Label != "fix the parser" {
		t.Fatalf("list = %+v", l)
	}
	plan, _ := again.Plan(l[0].ID)
	if _, err := again.Apply(plan); err != nil {
		t.Fatal(err)
	}
	if f.read("a.txt") != "keep me" {
		t.Fatal("restore after reopen failed")
	}
}

func TestACorruptBlobIsNotWritten(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "good")
	f.tr.BeginTurn("", "p")
	_ = f.tr.Save(a)
	f.write("a.txt", "changed")
	ents, _ := os.ReadDir(filepath.Join(f.tr.Dir(), "blobs"))
	if err := os.WriteFile(filepath.Join(f.tr.Dir(), "blobs", ents[0].Name()), []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, res := f.restoreLatest()
	if res.Restored != 0 || f.read("a.txt") != "changed" {
		t.Fatalf("a blob that fails its hash was written: %+v", res)
	}
}

func TestLabelsAndEntryIDsAreCleaned(t *testing.T) {
	f := newFixture(t, Limits{})
	f.tr.BeginTurn("bad id with spaces\n", "line one\n\x1b[31mline two"+strings.Repeat("x", 300))
	_ = f.tr.Save(f.write("a.txt", "x"))
	l := f.tr.List()[0]
	if l.Entry != "" {
		t.Fatalf("entry = %q, a malformed id must be dropped", l.Entry)
	}
	if strings.ContainsAny(l.Label, "\n\x1b") || len(l.Label) > 100 {
		t.Fatalf("label = %q", l.Label)
	}
}

func TestSweepRemovesOnlyOldSessionsAndNeverFollowsLinks(t *testing.T) {
	root := t.TempDir()
	mk := func(proj, sess string, age time.Duration) string {
		d := filepath.Join(root, proj, sess)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "index.jsonl"), []byte("x"), 0o600)
		old := time.Now().Add(-age)
		_ = os.Chtimes(filepath.Join(d, "index.jsonl"), old, old)
		_ = os.Chtimes(d, old, old)
		return d
	}
	oldDir := mk("p-1", "old", 10*24*time.Hour)
	freshDir := mk("p-1", "fresh", time.Hour)
	keepDir := mk("p-1", "keep", 30*24*time.Hour)
	lonely := mk("p-2", "old2", 20*24*time.Hour)

	target := t.TempDir()
	_ = os.WriteFile(filepath.Join(target, "precious"), []byte("x"), 0o600)
	link := filepath.Join(root, "p-1", "linked")
	if err := os.Symlink(target, link); err == nil {
		old := time.Now().Add(-40 * 24 * time.Hour)
		_ = os.Chtimes(link, old, old)
	}

	if n := Sweep(root, 7, time.Now(), "keep"); n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	for _, d := range []string{oldDir, lonely} {
		if _, err := os.Stat(d); err == nil {
			t.Fatalf("%s survived", d)
		}
	}
	for _, d := range []string{freshDir, keepDir, filepath.Join(target, "precious")} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s was removed: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "p-2")); err == nil {
		t.Fatal("an empty project directory was left behind")
	}
	if Sweep(root, 0, time.Now(), "") != 0 {
		t.Fatal("a zero retention must remove nothing")
	}
}

func TestOpenValidatesItsArguments(t *testing.T) {
	if _, err := Open("", Limits{MaxPerSession: 1, MaxBytes: 1}, Policy{}); err == nil {
		t.Fatal("empty dir accepted")
	}
	if _, err := Open(t.TempDir(), Limits{}, Policy{}); err == nil {
		t.Fatal("zero limits accepted")
	}
}

func TestNoPolicyRefusesEveryRestore(t *testing.T) {
	f := newFixture(t, Limits{})
	a := f.write("a.txt", "orig")
	f.tr.BeginTurn("", "p")
	_ = f.tr.Save(a)
	f.write("a.txt", "changed")
	f.tr.SetPolicy(Policy{})
	_, res := f.restoreLatest()
	if res.Restored != 0 || f.read("a.txt") != "changed" {
		t.Fatalf("a tracker with no confinement policy restored: %+v", res)
	}
}

// TestDocNamesEveryExportedFunction keeps docs/checkpoints.md in step with the
// package's surface.
func TestDocNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, "docs/checkpoints.md", docparity.ExportedFuncs(t, "."))
}

func TestBaselineWithAnUnusualPathIsCountedNotFatal(t *testing.T) {
	f := newFixture(t, Limits{})
	f.tr.BeginTurn("", "p")
	good := f.write("good.txt", "g0")
	err := f.tr.SaveBaselines([]Baseline{
		{Path: filepath.Join(f.root, "we\x1bird.txt"), Existed: true, Known: true, Body: []byte("x")},
		{Path: "relative.txt"},
		{Path: good, Existed: true, Known: true, Body: []byte("g0")},
	})
	if err != nil {
		t.Fatalf("an unusual name failed the whole record: %v", err)
	}
	l := f.tr.List()
	if len(l) != 1 || l[0].Files != 1 || l[0].Gaps != 1 {
		t.Fatalf("list = %+v, want the good file and one gap note", l)
	}
}
