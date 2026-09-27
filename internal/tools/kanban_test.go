package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/permissions"
)

func kanbanBase(t *testing.T) KanbanBase {
	t.Helper()
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	src := kanban.NewSource(kanban.Provenance{SessionID: "sess-live", HostID: "h", Project: "belai", ProjectKey: "belai-1", Dir: "/src/belai"})
	return KanbanBase{Store: store, Source: src}
}

func TestKanbanKinds(t *testing.T) {
	if !KindKanban.NeedsClassifier() {
		t.Fatal("KanbanSearch results must classify")
	}
	if KindKanbanWrite.NeedsClassifier() {
		t.Fatal("kanban write confirmations are harness-composed")
	}
	b := kanbanBase(t)
	for _, tool := range []Tool{KanbanSearch{b}, KanbanUpdate{b}, KanbanMove{KanbanBase: b, Allowed: KanbanLoopLists}, KanbanAdd{b}} {
		if Mutates(tool) {
			t.Errorf("%s mutates the workspace", tool.Definition().Name)
		}
		if tool.Subject(map[string]any{"id": "x"}) != "" {
			t.Errorf("%s has a permission subject", tool.Definition().Name)
		}
		for name := range tool.Definition().Properties {
			if strings.Contains(name, "path") || name == "session" || name == "project_key" || name == "dir" {
				t.Errorf("%s takes a %q argument", tool.Definition().Name, name)
			}
		}
	}
}

func TestKanbanAddStampsProvenanceAndDedupes(t *testing.T) {
	b := kanbanBase(t)
	ctx := context.Background()
	res, err := KanbanAdd{b}.Execute(ctx, map[string]any{
		"title": "add tests for the sync worker", "body": "sync.go has none", "category": "unverified",
		// Provenance in the arguments is ignored.
		"session_id": "forged", "project": "evil", "list": "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindKanbanWrite || !strings.Contains(res.Content, "added to review") {
		t.Fatalf("result %+v", res)
	}
	items, _ := b.Store.Search(kanban.Query{})
	if len(items) != 1 {
		t.Fatalf("items %d", len(items))
	}
	it := items[0]
	if it.List != kanban.Review || it.SessionID != "sess-live" || it.Project != "belai" || it.Dir != "/src/belai" {
		t.Fatalf("provenance not stamped by the harness: %+v", it)
	}
	if !strings.Contains(it.Body, "category: unverified") {
		t.Fatalf("category missing: %q", it.Body)
	}
	res, err = KanbanAdd{b}.Execute(ctx, map[string]any{"title": "Add tests for the sync worker"})
	if err != nil || !strings.Contains(res.Content, "already on the board as "+it.Short()) {
		t.Fatalf("dedupe: %+v %v", res, err)
	}
	// The session id is read at call time.
	b.Source.SetSession("sess-next")
	KanbanAdd{b}.Execute(ctx, map[string]any{"title": "another"})
	got, _ := b.Store.Search(kanban.Query{Text: "another"})
	if len(got) != 1 || got[0].SessionID != "sess-next" {
		t.Fatalf("source not live: %+v", got)
	}
}

func TestKanbanMoveRespectsAllowed(t *testing.T) {
	b := kanbanBase(t)
	it, _, _ := b.Store.Add(kanban.ItemInput{Title: "t", List: kanban.Review}, b.Source.Get())
	wrap := KanbanMove{KanbanBase: b, Allowed: KanbanWrapUpLists}
	if enum := wrap.Definition().Properties["to"].Enum; len(enum) != 1 || enum[0] != "done" {
		t.Fatalf("wrap-up enum %v", enum)
	}
	if _, err := wrap.Execute(context.Background(), map[string]any{"id": it.Short(), "to": "in_progress"}); err == nil {
		t.Fatal("wrap-up moved an item to in_progress")
	}
	loop := KanbanMove{KanbanBase: b, Allowed: KanbanLoopLists}
	res, err := loop.Execute(context.Background(), map[string]any{"id": it.Short(), "to": "in progress", "note": "starting"})
	if err != nil || !strings.Contains(res.Content, "review → in_progress") {
		t.Fatalf("loop move: %+v %v", res, err)
	}
	if _, err := loop.Execute(context.Background(), map[string]any{"id": it.Short(), "to": "backlog"}); err == nil {
		t.Fatal("loop moved an item to backlog")
	}
	if _, err := loop.Execute(context.Background(), map[string]any{"id": "K-ffffff", "to": "done"}); err == nil || !strings.Contains(err.Error(), "KanbanSearch") {
		t.Fatalf("missing id error: %v", err)
	}
}

func TestKanbanSearchScopesAndRenders(t *testing.T) {
	b := kanbanBase(t)
	p := b.Source.Get()
	b.Store.Add(kanban.ItemInput{Title: "here", List: kanban.Blocked}, p)
	done, _, _ := b.Store.Add(kanban.ItemInput{Title: "finished", List: kanban.Review}, p)
	b.Store.Move(done.ID, kanban.Done, "", "")
	other := p
	other.Project, other.ProjectKey = "cli", "cli-1"
	b.Store.Add(kanban.ItemInput{Title: "elsewhere"}, other)
	s := KanbanSearch{b}
	ctx := context.Background()

	res, err := s.Execute(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindKanban || !strings.Contains(res.Content, "here") || strings.Contains(res.Content, "elsewhere") || strings.Contains(res.Content, "finished") {
		t.Fatalf("default scope: %s", res.Content)
	}
	res, _ = s.Execute(ctx, map[string]any{"project": "all"})
	if !strings.Contains(res.Content, "elsewhere") {
		t.Fatalf("all projects: %s", res.Content)
	}
	res, _ = s.Execute(ctx, map[string]any{"lists": []any{"done"}})
	if !strings.Contains(res.Content, "finished") || !strings.Contains(res.Content, "history:") {
		t.Fatalf("done list, single match detail: %s", res.Content)
	}
	if _, err := s.Execute(ctx, map[string]any{"lists": "later"}); err == nil {
		t.Fatal("unknown list accepted")
	}
}

func TestWithKanbanSurfaces(t *testing.T) {
	b := kanbanBase(t)
	reg := Default(t.TempDir(), false).WithKanban(b.Store, b.Source)
	for _, name := range []string{KanbanSearchName, KanbanUpdateName} {
		if _, ok := reg.Find(name); !ok {
			t.Fatalf("%s missing", name)
		}
		if _, ok := reg.Plan().Find(name); !ok {
			t.Fatalf("%s missing from plan mode", name)
		}
		if _, ok := reg.PlanWith(PlanSurface{Perms: permissions.Settings{}}).Find(name); !ok {
			t.Fatalf("%s missing from the plan surface", name)
		}
		if _, ok := reg.ReadOnlySurface().Find(name); !ok {
			t.Fatalf("%s missing from the read-only surface", name)
		}
	}
	if _, ok := KanbanOf(reg); !ok {
		t.Fatal("KanbanOf did not find the board")
	}
	if got := Default(t.TempDir(), false).WithKanban(nil, nil); len(got.Names()) != len(Default(t.TempDir(), false).Names()) {
		t.Fatal("a nil store added tools")
	}
}
