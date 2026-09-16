package promptlib

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	lib, err := load(filepath.Join(t.TempDir(), "no-such-prompts.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lib.Entries) != 0 {
		t.Fatalf("expected empty library, got %d entries", len(lib.Entries))
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompts.json")
	lib := Library{Entries: []Entry{
		{Name: "hello", Prompt: "say hello", CreatedAt: 42},
	}}
	if err := save(path, lib); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "hello" {
		t.Fatalf("got %+v", got.Entries)
	}
}

func TestMergeProjectOverridesGlobal(t *testing.T) {
	global := Library{Entries: []Entry{
		{Name: "a", Prompt: "global a"},
		{Name: "b", Prompt: "global b"},
	}}
	project := Library{Entries: []Entry{
		{Name: "b", Prompt: "project b"},
		{Name: "c", Prompt: "project c"},
	}}
	merged := Merge(global, project)
	if len(merged.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(merged.Entries))
	}
	order := []string{}
	for _, e := range merged.Entries {
		order = append(order, e.Name+":"+e.Prompt)
	}
	want := []string{"a:global a", "b:project b", "c:project c"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestFilterCaseInsensitive(t *testing.T) {
	lib := Library{Entries: []Entry{
		{Name: "Deploy", Prompt: "how to deploy"},
		{Name: "Test", Prompt: "run unit tests"},
		{Name: "deploy-prod", Prompt: "ship it"},
	}}
	results := lib.Filter("deploy")
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Name != "Deploy" {
		t.Fatalf("first result = %q", results[0].Name)
	}
}

func TestFilterEmptyQueryReturnsAll(t *testing.T) {
	lib := Library{Entries: []Entry{
		{Name: "a", Prompt: "a"},
		{Name: "b", Prompt: "b"},
	}}
	results := lib.Filter("")
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestPromptsExtractsStrings(t *testing.T) {
	entries := []Entry{
		{Name: "a", Prompt: "prompt a"},
		{Name: "b", Prompt: "prompt b"},
	}
	got := Prompts(entries)
	want := []string{"prompt a", "prompt b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestAddReplacesDuplicateName(t *testing.T) {
	lib := Library{Entries: []Entry{
		{Name: "a", Prompt: "old"},
	}}
	lib.Add(Entry{Name: "a", Prompt: "new"})
	if len(lib.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(lib.Entries))
	}
	if lib.Entries[0].Prompt != "new" {
		t.Fatalf("prompt = %q", lib.Entries[0].Prompt)
	}
	if lib.Entries[0].CreatedAt == 0 {
		t.Fatalf("CreatedAt should be set")
	}
}

// Add stamps CreatedAt only when it is zero, so re-saving an imported or
// hand-edited entry does not rewrite when it was first created.
func TestAddPreservesExplicitCreatedAt(t *testing.T) {
	lib := Library{}
	lib.Add(Entry{Name: "a", Prompt: "a", CreatedAt: 42})
	if lib.Entries[0].CreatedAt != 42 {
		t.Fatalf("CreatedAt = %d, want 42", lib.Entries[0].CreatedAt)
	}
	lib.Add(Entry{Name: "a", Prompt: "b", CreatedAt: 42})
	if len(lib.Entries) != 1 || lib.Entries[0].CreatedAt != 42 {
		t.Fatalf("replace lost CreatedAt: %+v", lib.Entries)
	}
}

func TestAddAppendsNewName(t *testing.T) {
	lib := Library{Entries: []Entry{{Name: "a", Prompt: "a"}}}
	lib.Add(Entry{Name: "b", Prompt: "b"})
	if len(lib.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(lib.Entries))
	}
}

func TestRemove(t *testing.T) {
	lib := Library{Entries: []Entry{
		{Name: "a", Prompt: "a"},
		{Name: "b", Prompt: "b"},
	}}
	if !lib.Remove("a") {
		t.Fatalf("expected Remove to return true")
	}
	if len(lib.Entries) != 1 || lib.Entries[0].Name != "b" {
		t.Fatalf("got %+v", lib.Entries)
	}
	if lib.Remove("x") {
		t.Fatalf("expected Remove to return false")
	}
}

func TestSaveCreatesDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "nested")
	path := filepath.Join(dir, "prompts.json")
	lib := Library{Entries: []Entry{{Name: "x", Prompt: "x"}}}
	if err := save(path, lib); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestMatchAgainstPrompt(t *testing.T) {
	e := Entry{Name: "x", Prompt: "deploy to production"}
	if !Match(e, "production") {
		t.Fatalf("expected match against prompt text")
	}
	if Match(e, "staging") {
		t.Fatalf("expected no match")
	}
}
