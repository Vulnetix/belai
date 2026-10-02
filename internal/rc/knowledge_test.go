package rc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/knowledge/tags"
	"github.com/vulnetix/belai/internal/sessionsync"
)

const secretBody = "AKIAIOSFODNN7EXAMPLE the deploy key is hunter2"

func indexWith(t *testing.T, address string) *knowledge.Index {
	t.Helper()
	ix := knowledge.NewIndex("reviewer")
	in := knowledge.Input{
		Address: address, Source: "/home/u/docs/runbook.md", Size: int64(len(secretBody)), ModTime: time.Now(),
		SHA: strings.Repeat("a", 64), Chunks: []knowledge.RawChunk{{Text: secretBody}},
	}
	if _, err := ix.Ingest(context.Background(), in, func(context.Context, string) (bool, error) { return true, nil }, 100000); err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestKnowledgeCatalogueCarriesFactsAndNoText(t *testing.T) {
	addr := knowledge.Address(knowledge.ProjectScope, "docs/runbook.md")
	ix := indexWith(t, addr)
	dir := t.TempDir()
	if err := os.WriteFile(knowledge.Path(dir), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	loads := 0
	load := func() knowledge.IndexInfo { loads++; return knowledge.IndexInfo{Index: ix} }
	head := sessionsync.RCKnowledge{Scope: knowledge.ScopeProject, Name: "api", Root: "/src/api"}
	got, ok := cachedKnowledge(dir, load, head)
	if !ok || len(got.Docs) != 1 || got.Docs[0].Address != addr || got.Docs[0].SHA256 != strings.Repeat("a", 64) || got.Root != "/src/api" {
		t.Fatalf("catalogue = %+v", got)
	}
	b, _ := json.Marshal(got)
	for _, leak := range []string{"AKIA", "hunter2", "/home/u/docs"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("the catalogue carries %q: %s", leak, b)
		}
	}
	// An unchanged file is not loaded again; a changed one is.
	if _, ok := cachedKnowledge(dir, load, head); !ok || loads != 1 {
		t.Fatalf("an unchanged index was reloaded (%d loads)", loads)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(knowledge.Path(dir), later, later); err != nil {
		t.Fatal(err)
	}
	if _, ok := cachedKnowledge(dir, load, head); !ok || loads != 2 {
		t.Fatalf("a changed index was not reloaded (%d loads)", loads)
	}
	// No file, no index.
	if _, ok := cachedKnowledge(filepath.Join(dir, "none"), load, head); ok {
		t.Fatal("a missing index was listed")
	}
}

func TestKnowledgeDocsKeepOnlyHarnessShapes(t *testing.T) {
	good := knowledge.Address(knowledge.ProjectScope, "a.md")
	docs := knowledgeDocs([]knowledge.Doc{
		{Address: good, SHA: strings.Repeat("b", 64), Size: 10, Tags: tags.Result{
			Kind: "doc", Lang: "markdown",
			Labels: []string{"type:doc", "ext:md", "Bad Label", "x:" + strings.Repeat("y", 80)},
			Topics: []tags.Topic{{ID: "auth"}, {ID: "has space"}},
		}},
		{Address: "/etc/passwd", SHA: strings.Repeat("c", 64)},
		{Address: good + "2", SHA: "not-a-sha"},
	})
	if len(docs) != 1 {
		t.Fatalf("docs = %+v", docs)
	}
	d := docs[0]
	if strings.Join(d.Labels, ",") != "type:doc,ext:md" || strings.Join(d.Topics, ",") != "auth" || d.Kind != "doc" || d.Lang != "markdown" {
		t.Fatalf("doc = %+v", d)
	}
}

func TestLocalKnowledgeCapsWhatOneAdvertisementCarries(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if got := localKnowledge(nil); len(got) != 0 {
		t.Fatalf("a host with no index advertises %+v", got)
	}
}
