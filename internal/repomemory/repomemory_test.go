package repomemory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/gittest"
)

const agentID = "11111111-1111-4111-8111-111111111111:22222222-2222-4222-8222-222222222222"

func TestScopedByRepoKey(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir, "vulnetix/belai")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Open(dir, "vulnetix/other")
	c, _ := Open(dir, "path:0123456789abcdef")
	if _, err := a.Append("A owns internal/x/", []string{"x"}, Stamp{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Append("pathless repo note", nil, Stamp{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Search("", 0); len(got) != 0 {
		t.Fatalf("other repo sees %v", got)
	}
	if got, _ := a.Search("owns", 0); len(got) != 1 || got[0].AgentID != agentID {
		t.Fatalf("search = %+v", got)
	}
	for _, bad := range []string{"", "../etc", "Owner/Name", "a", "path:zz", "a/b/../c"} {
		if _, err := Open(dir, bad); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "repos"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "/") || filepath.Dir(filepath.Join(dir, e.Name())) != dir {
			t.Fatalf("file outside the store: %s", e.Name())
		}
	}
}

func TestTwoWritersAndPruning(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir, "o/r")
	b, _ := Open(dir, "o/r")
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		s := a
		if i == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < MaxNotes; j++ {
				if _, err := s.Append("note", nil, Stamp{}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	notes, err := a.read()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != MaxNotes {
		t.Fatalf("%d notes kept, want %d", len(notes), MaxNotes)
	}
	if got, _ := a.Search("", 1000); len(got) != MaxLimit {
		t.Fatalf("limit not capped: %d", len(got))
	}
}

func TestNoteCleanedAndStamped(t *testing.T) {
	s, _ := Open(t.TempDir(), "o/r")
	n, err := s.Append("hi\x1b[31m there"+strings.Repeat("x", 2*MaxNoteBytes), []string{"Docs", "bad tag!", "docs"},
		Stamp{AgentID: agentID, SessionID: "s", MessageID: "a2a-01k8z3m5x6y7z8a9b0c1d2e3f4", ContextID: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Text) > MaxNoteBytes || strings.Contains(n.Text, "\x1b") {
		t.Fatalf("text not cleaned: %d bytes", len(n.Text))
	}
	if strings.Join(n.Tags, ",") != "docs" || n.MessageID == "" || n.ContextID != "" || n.CreatedAt == 0 || n.NoteID == "" {
		t.Fatalf("note = %+v", n)
	}
	if _, err := s.Append("  ", nil, Stamp{}); err == nil {
		t.Fatal("empty note stored")
	}
	if !strings.Contains(Format("o/r", []Note{n}), "re a2a-01k8z3m5x6y7z8a9b0c1d2e3f4") {
		t.Fatal("format drops the message link")
	}
}

func TestValidScopeKey(t *testing.T) {
	cases := []struct {
		scope string
		key   string
		want  bool
	}{
		{ScopeRepo, "vulnetix/belai", true},
		{ScopeRepo, "a/b/c", true},
		{ScopeRepo, "a/b/c/d", true},
		{ScopeRepo, "a/b/c/d/e", false},
		{ScopeRepo, "path:0123456789abcdef", true},
		{ScopeRepo, "path:gg", false},
		{ScopeRepo, "Owner/Name", false},
		{ScopeRepo, "a/b/../c", false},
		{ScopeRepo, "a/b__c", false},
		{ScopeRepo, "", false},
		{ScopeProfile, "belai-builder", true},
		{ScopeProfile, "123abc", true},
		{ScopeProfile, "UPPER", false},
		{ScopeProfile, "a_b", false},
		{ScopeProfile, strings.Repeat("a", 81), false},
		{"other", "anything", false},
	}
	for _, tc := range cases {
		if got := ValidScopeKey(tc.scope, tc.key); got != tc.want {
			t.Errorf("ValidScopeKey(%q, %q) = %v, want %v", tc.scope, tc.key, got, tc.want)
		}
	}
}

func TestProfileKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"belai:builder", "belai-builder"},
		{"  Foo Bar  ", "foo-bar"},
		{"a__b", "a-b"},
		{"-leading-", "leading"},
		{strings.Repeat("a", 90), strings.Repeat("a", 80)},
		{"", ""},
		{"!!!", ""},
	}
	for _, tc := range cases {
		if got := ProfileKey(tc.in); got != tc.want {
			t.Errorf("ProfileKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := OpenProfile(t.TempDir(), "belai:builder"); err != nil {
		t.Errorf("OpenProfile rejected a profile name: %v", err)
	}
}

func TestRepoKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := RepoKey("github.com/vulnetix/belai", root); got != "github.com/vulnetix/belai" {
		t.Errorf("RepoKey(remote) = %q", got)
	}
	if got := RepoKey("Vulnetix/Belai/", root); got != "vulnetix/belai" {
		t.Errorf("RepoKey(mixed case) = %q", got)
	}
	// An explicit path: key is never taken as a remote; the root decides.
	if got := RepoKey("path:0123456789abcdef", root); !strings.HasPrefix(got, "path:") {
		t.Errorf("RepoKey(path: remote) = %q, want the root's path: key", got)
	}
	if got := RepoKey("../etc", root); !strings.HasPrefix(got, "path:") {
		t.Errorf("RepoKey(bad remote) = %q, want the root's path: key", got)
	}
	if got := RepoKey("", ""); got != "" {
		t.Errorf("RepoKey(no remote, no root) = %q", got)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestKeyForDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	if got := KeyForDir(""); got != "" {
		t.Errorf("KeyForDir(empty) = %q", got)
	}
	gittest.Isolate(t)

	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init")
	gitIn(t, repo, "remote", "add", "origin", "git@github.com:vulnetix/belai.git")
	if got := KeyForDir(repo); got != "vulnetix/belai" {
		t.Errorf("KeyForDir(repo) = %q, want vulnetix/belai", got)
	}
	sub := filepath.Join(repo, "subdir")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := KeyForDir(sub); got != "vulnetix/belai" {
		t.Errorf("KeyForDir(subdir) = %q, want vulnetix/belai", got)
	}

	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := KeyForDir(outside); !strings.HasPrefix(got, "path:") {
		t.Errorf("KeyForDir(outside a repository) = %q, want a path: key", got)
	}
}

func TestClear(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Unix(1700000000, 0).UTC()
	s, _ := Open(dir, "o/r")
	s = s.At(fixed)
	n1, err := s.Append("note one", nil, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := s.Append("note two", nil, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	if notes, _ := s.All(); len(notes) != 2 {
		t.Fatalf("%d notes before Clear, want 2", len(notes))
	}

	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if notes, _ := s.All(); len(notes) != 0 {
		t.Fatalf("%d notes after Clear, want 0", len(notes))
	}
	if _, err := os.Stat(s.path()); !os.IsNotExist(err) {
		t.Fatalf("store file should be gone: %v", err)
	}

	// Clear queues a tombstone per note; the outbox also holds the appends.
	out, _, _ := Outbox(dir, 0, 100)
	gone := map[string]Note{}
	for _, n := range out {
		if n.DeletedAt != 0 {
			gone[n.NoteID] = n
		}
	}
	if len(gone) != 2 {
		t.Fatalf("%d tombstones queued, want 2: %+v", len(gone), gone)
	}
	for _, n := range gone {
		if n.Text != "" || n.Seq != 0 || n.DeletedAt != fixed.UnixMilli() || n.UpdatedAt != fixed.UnixMilli() {
			t.Errorf("bad tombstone: %+v", n)
		}
	}
	if _, ok := gone[n1.NoteID]; !ok {
		t.Errorf("note one has no tombstone: %+v", gone)
	}
	if _, ok := gone[n2.NoteID]; !ok {
		t.Errorf("note two has no tombstone: %+v", gone)
	}

	if err := s.Clear(); err != nil {
		t.Fatalf("clearing an empty store: %v", err)
	}
}

func TestEmpty(t *testing.T) {
	dir := t.TempDir()
	if !Empty(dir) {
		t.Fatal("a fresh root is not empty")
	}
	for _, sub := range []string{"repos", "profiles"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if !Empty(dir) {
		t.Fatal("scope directories without notes are not empty")
	}
	for _, sub := range []string{"repos", "profiles"} {
		f, err := os.Create(filepath.Join(dir, sub, "scope.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if Empty(dir) {
			t.Fatalf("a .jsonl in %s leaves the root empty", sub)
		}
		if err := os.Remove(filepath.Join(dir, sub, "scope.jsonl")); err != nil {
			t.Fatal(err)
		}
	}
	if !Empty(dir) {
		t.Fatal("removing the notes leaves the root not empty")
	}
}

func TestOutboxWalkAndCompact(t *testing.T) {
	root := Root(t.TempDir())

	notes, off, err := Outbox(root, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 || off != 0 {
		t.Fatalf("missing outbox: %d notes, offset %d", len(notes), off)
	}

	s, _ := Open(root, "o/r")
	var ids []string
	for i := 0; i < 3; i++ {
		n, err := s.Append(fmt.Sprintf("note %d", i), nil, Stamp{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, n.NoteID)
	}

	// Offset walking: each read resumes where the last ended.
	notes, next, _ := Outbox(root, 0, 2)
	if len(notes) != 2 {
		t.Fatalf("limit 2 returned %d notes", len(notes))
	}
	tail, end, _ := Outbox(root, next, 10)
	if len(tail) != 1 {
		t.Fatalf("the tail returned %d notes", len(tail))
	}
	if got := notes[0].NoteID + notes[1].NoteID + tail[0].NoteID; got != strings.Join(ids, "") {
		t.Fatalf("outbox order = %s, want %s", got, strings.Join(ids, ""))
	}
	if info, err := os.Stat(filepath.Join(root, outboxFile)); err != nil || info.Size() != end {
		t.Fatalf("offset %d does not match the file size %v, %v", end, info, err)
	}

	// An offset past the file means it was truncated: reads restart at 0.
	if all, back, _ := Outbox(root, end+1, 10); len(all) != 3 || back == 0 {
		t.Fatalf("offset reset: %d notes, offset %d", len(all), back)
	}

	// Compaction needs the offset to match the size and be at least 1 MiB.
	if CompactOutbox(root, end) {
		t.Fatal("a small outbox was compacted")
	}
	if CompactOutbox(root, 1<<20) {
		t.Fatal("an outbox smaller than the offset was compacted")
	}

	// A big outbox is truncated once its bytes were all uploaded.
	big := Note{NoteID: NewNoteID(time.UnixMilli(1)), Scope: ScopeRepo, ScopeKey: "o/r", CreatedAt: 1,
		Text: strings.Repeat("a", 1000)}
	line, err := json.Marshal(big)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for buf.Len() < 1<<20 {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(root, outboxFile), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	all, end, _ := Outbox(root, 0, 1<<20)
	if len(all) == 0 {
		t.Fatal("the big outbox read as empty")
	}
	if !CompactOutbox(root, end) {
		t.Fatal("a fully uploaded outbox was not compacted")
	}
	if info, err := os.Stat(filepath.Join(root, outboxFile)); err != nil || info.Size() != 0 {
		t.Fatalf("the outbox was not truncated: %v, %v", info, err)
	}
	if all, off, _ := Outbox(root, 0, 10); len(all) != 0 || off != 0 {
		t.Fatalf("the compacted outbox still reads: %d notes, offset %d", len(all), off)
	}
}

func TestApply(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Unix(1700000000, 0).UTC()
	s, _ := Open(dir, "o/r")
	s = s.At(fixed)
	n1, err := s.Append("local note", []string{"a"}, Stamp{})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := s.Append("second note", nil, Stamp{})
	if err != nil {
		t.Fatal(err)
	}

	// A note that cannot be kept changes nothing.
	if changed, err := Apply(dir, []Note{{NoteID: "nope", Scope: ScopeRepo, ScopeKey: "o/r", Text: "x", CreatedAt: 1}}); err != nil || changed != 0 {
		t.Fatalf("Apply(unclean note) = %d, %v, want 0, nil", changed, err)
	}

	// A new pulled note is upserted.
	pulledNew := Note{NoteID: NewNoteID(fixed.Add(time.Hour)), Scope: ScopeRepo, ScopeKey: "o/r",
		Text: "pulled new", CreatedAt: fixed.Add(time.Hour).UnixMilli()}
	if changed, err := Apply(dir, []Note{pulledNew}); err != nil || changed != 1 {
		t.Fatalf("Apply(new) = %d, %v, want 1, nil", changed, err)
	}
	if got, _ := s.All(); len(got) != 3 {
		t.Fatalf("%d notes after the upsert, want 3", len(got))
	}

	// The newer version of a local note wins.
	newer := n1
	newer.Text = "updated local note"
	newer.UpdatedAt = fixed.Add(2 * time.Hour).UnixMilli()
	if changed, err := Apply(dir, []Note{newer}); err != nil || changed != 1 {
		t.Fatalf("Apply(update) = %d, %v, want 1, nil", changed, err)
	}
	if got, _ := s.Search("updated", 10); len(got) != 1 || got[0].NoteID != n1.NoteID {
		t.Fatalf("the update did not win: %+v", got)
	}

	// The older version of a note loses.
	stale := Note{NoteID: n2.NoteID, Scope: ScopeRepo, ScopeKey: "o/r", Text: "stale",
		CreatedAt: fixed.Add(-time.Minute).UnixMilli()}
	if changed, err := Apply(dir, []Note{stale}); err != nil || changed != 0 {
		t.Fatalf("Apply(stale) = %d, %v, want 0, nil", changed, err)
	}
	if got, _ := s.Search("stale", 10); len(got) != 0 {
		t.Fatalf("the stale copy was stored: %+v", got)
	}

	// A tombstone removes the note.
	tombstone := Note{NoteID: n2.NoteID, Scope: ScopeRepo, ScopeKey: "o/r", DeletedAt: fixed.Add(3 * time.Hour).UnixMilli()}
	if changed, err := Apply(dir, []Note{tombstone}); err != nil || changed != 1 {
		t.Fatalf("Apply(tombstone) = %d, %v, want 1, nil", changed, err)
	}
	if got, _ := s.All(); len(got) != 2 {
		t.Fatalf("%d notes after the tombstone, want 2", len(got))
	}
	if got, _ := s.Search("second", 10); len(got) != 0 {
		t.Fatalf("the tombstoned note is still live: %+v", got)
	}

	// A pull past MaxNotes prunes the oldest.
	base := fixed.Add(4 * time.Hour)
	bulk := make([]Note, MaxNotes+10)
	for i := range bulk {
		at := base.Add(time.Duration(i) * time.Minute)
		bulk[i] = Note{NoteID: NewNoteID(at), Scope: ScopeRepo, ScopeKey: "o/r",
			Text: fmt.Sprintf("bulk %d", i), CreatedAt: at.UnixMilli()}
	}
	if changed, err := Apply(dir, bulk); err != nil || changed != 1 {
		t.Fatalf("Apply(bulk) = %d, %v, want 1, nil", changed, err)
	}
	got, _ := s.All()
	if len(got) != MaxNotes {
		t.Fatalf("%d notes after the bulk pull, want %d", len(got), MaxNotes)
	}
	if got[0].Text != "bulk 10" {
		t.Fatalf("the oldest note kept is %q, want %q", got[0].Text, "bulk 10")
	}
	if last := got[len(got)-1].Text; last != fmt.Sprintf("bulk %d", MaxNotes+9) {
		t.Fatalf("the newest note kept is %q", last)
	}

	// Re-applying what is already stored leaves the store as it was.
	if _, err := Apply(dir, got[:10]); err != nil {
		t.Fatalf("Apply(what is stored): %v", err)
	}
	again, _ := s.All()
	if len(again) != len(got) {
		t.Fatalf("%d notes after re-applying, want %d", len(again), len(got))
	}
	for i := range got {
		if !reflect.DeepEqual(again[i], got[i]) {
			t.Fatalf("note %d changed on re-apply: %+v, want %+v", i, again[i], got[i])
		}
	}
}
