package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readStateTools(t *testing.T) (root string, read *Read, edit *Edit, write *Write) {
	t.Helper()
	root = t.TempDir()
	cwd := NewCwd(root)
	reads := NewReadState()
	return root, &Read{Root: root, Cwd: cwd, Reads: reads}, &Edit{Root: root, Cwd: cwd, Reads: reads}, &Write{Root: root, Cwd: cwd, Reads: reads}
}

func TestEditRequiresARead(t *testing.T) {
	root, read, edit, _ := readStateTools(t)
	p := filepath.Join(root, "a.txt")
	_ = os.WriteFile(p, []byte("hello world\n"), 0o600)
	args := map[string]any{"file_path": "a.txt", "old_string": "world", "new_string": "there"}

	if _, err := edit.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "has not been read") {
		t.Fatalf("unread edit: err = %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "hello world\n" {
		t.Fatal("a refused edit must leave the file untouched")
	}
	if _, err := read.Execute(context.Background(), map[string]any{"file_path": "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := edit.Execute(context.Background(), args); err != nil {
		t.Fatalf("edit after read: %v", err)
	}
	// The harness wrote those bytes, so a follow-up edit needs no re-read.
	if _, err := edit.Execute(context.Background(), map[string]any{"file_path": "a.txt", "old_string": "there", "new_string": "again"}); err != nil {
		t.Fatalf("consecutive edit: %v", err)
	}
}

func TestEditRefusesAFileChangedSinceTheRead(t *testing.T) {
	root, read, edit, _ := readStateTools(t)
	p := filepath.Join(root, "a.txt")
	_ = os.WriteFile(p, []byte("one\n"), 0o600)
	if _, err := read.Execute(context.Background(), map[string]any{"file_path": "a.txt"}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p, []byte("one two\n"), 0o600) // a formatter, Bash, the user
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(p, later, later)
	_, err := edit.Execute(context.Background(), map[string]any{"file_path": "a.txt", "old_string": "one", "new_string": "1"})
	if err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("stale edit: err = %v", err)
	}
}

func TestWriteGuardsOnlyExistingFiles(t *testing.T) {
	root, read, _, write := readStateTools(t)
	if _, err := write.Execute(context.Background(), map[string]any{"file_path": "new.txt", "content": "x"}); err != nil {
		t.Fatalf("a new file needs no read: %v", err)
	}
	if _, err := write.Execute(context.Background(), map[string]any{"file_path": "new.txt", "content": "y"}); err != nil {
		t.Fatalf("rewriting a file this session wrote: %v", err)
	}
	_ = os.WriteFile(filepath.Join(root, "old.txt"), []byte("keep"), 0o600)
	if _, err := write.Execute(context.Background(), map[string]any{"file_path": "old.txt", "content": "gone"}); err == nil {
		t.Fatal("overwriting an unread file must be refused")
	}
	if _, err := read.Execute(context.Background(), map[string]any{"file_path": "old.txt", "offset": 1, "limit": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := write.Execute(context.Background(), map[string]any{"file_path": "old.txt", "content": "gone"}); err != nil {
		t.Fatalf("a partial read counts as a read: %v", err)
	}
}

func TestNilReadStateDisablesTheGuard(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600)
	e := &Edit{Root: root, Cwd: NewCwd(root)}
	if _, err := e.Execute(context.Background(), map[string]any{"file_path": "a.txt", "old_string": "a", "new_string": "b"}); err != nil {
		t.Fatalf("no ReadState means no guard: %v", err)
	}
}

func TestDefaultRegistrySharesOneReadState(t *testing.T) {
	reg := Default(t.TempDir(), false)
	r, _ := reg.Find("Read")
	e, _ := reg.Find("Edit")
	w, _ := reg.Find("Write")
	rs := r.(*Read).Reads
	if rs == nil || e.(*Edit).Reads != rs || w.(*Write).Reads != rs {
		t.Fatal("Read, Edit and Write must share one ReadState")
	}
}
