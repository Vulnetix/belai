package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestCatalogLoadsProjectAndProfileIndexesReadOnly(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	pdir := mustDir(t)(config.ProjectKnowledgeDir(root))
	ix := NewIndex(ProjectScope)
	if _, err := ix.Ingest(context.Background(), doc("kb+project/.vulnetix/memory.yaml", "scan memory for the repository"), nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := ix.Save(pdir); err != nil {
		t.Fatal(err)
	}
	prof := NewIndex("reviewer")
	if _, err := prof.Ingest(context.Background(), doc("kb+reviewer/guide.md", "review checklist"), nil, 0); err != nil {
		t.Fatal(err)
	}
	profDir := mustDir(t)(config.ProfileKnowledgeDir(testProfileID))
	if err := prof.Save(profDir); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(Path(pdir))

	p := LoadProject(root)
	if p.Err != nil || p.Docs() != 1 || p.Scope != ScopeProject || !p.Exists() || p.Bytes == 0 || p.Root != root {
		t.Fatalf("project = %+v", p)
	}
	q := LoadProfile(testProfileID, "reviewer")
	if q.Err != nil || q.Docs() != 1 || q.Scope != ScopeProfile || q.Tokens() == 0 {
		t.Fatalf("profile = %+v", q)
	}
	after, _ := os.ReadFile(Path(pdir))
	if string(before) != string(after) {
		t.Fatal("loading through the catalogue changed the file")
	}
	keys, err := ProjectKeys()
	if err != nil || len(keys) != 1 || keys[0] != p.Key {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if k, err := ProjectKey(root); err != nil || k != p.Key {
		t.Fatalf("key=%q err=%v", k, err)
	}
}

func TestCatalogReportsACorruptIndexAndLeavesIt(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	dir := mustDir(t)(config.ProjectKnowledgeDir(root))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := []byte("BELAIKB1 this is not an index")
	if err := os.WriteFile(Path(dir), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	info := LoadProject(root)
	if !errors.Is(info.Err, ErrCorrupt) || info.Index != nil || info.Docs() != 0 || !info.Exists() {
		t.Fatalf("info = %+v", info)
	}
	if got, _ := os.ReadFile(Path(dir)); string(got) != string(bad) {
		t.Fatal("a corrupt index must be left as it is")
	}
}

func TestCatalogMissingIndexIsEmptyNotAnError(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	info := LoadProject(t.TempDir())
	if info.Err != nil || info.Index == nil || info.Docs() != 0 || info.Exists() {
		t.Fatalf("info = %+v", info)
	}
	if keys, err := ProjectKeys(); err != nil || len(keys) != 0 {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
}

func TestCatalogRefusesKeysThatAreNotKeys(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	for _, k := range []string{"", "../../etc", "ABCDEF", filepath.Join("a", "b")} {
		if info := LoadProjectKey(k, "x", ""); info.Err == nil || info.Index != nil {
			t.Errorf("project key %q accepted: %+v", k, info)
		}
	}
	for _, id := range []string{"", "../x", "not-a-uuid"} {
		if info := LoadProfile(id, "x"); info.Err == nil || info.Index != nil {
			t.Errorf("profile id %q accepted: %+v", id, info)
		}
	}
}
