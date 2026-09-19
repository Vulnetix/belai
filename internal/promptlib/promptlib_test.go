package promptlib

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/signet/internal/config"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	listing, err := Load(config.ScopeGlobal, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(listing.Entries) != 0 {
		t.Fatalf("expected empty library, got %d entries", len(listing.Entries))
	}
}

func TestCreateAndLoad(t *testing.T) {
	dir := t.TempDir()
	e, err := Create(config.ScopeProject, dir, "hello", "say hello")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if e.Name != "hello" {
		t.Fatalf("slug = %q, want hello", e.Name)
	}
	if !e.Enabled {
		t.Fatal("new entry should be enabled")
	}

	listing, err := Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Prompt != "say hello" {
		t.Fatalf("got %+v", listing.Entries)
	}
}

func TestCreateDuplicateReturnsErrNameExists(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(config.ScopeProject, dir, "hello", "one"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := Create(config.ScopeProject, dir, "hello", "two")
	if !errors.Is(err, ErrNameExists) {
		t.Fatalf("expected ErrNameExists, got %v", err)
	}
}

func TestUpdate(t *testing.T) {
	dir := t.TempDir()
	e, err := Create(config.ScopeProject, dir, "hello", "one")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := Update(e, "two")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Prompt != "two" {
		t.Fatalf("prompt = %q, want two", updated.Prompt)
	}
	listing, _ := Load(config.ScopeProject, dir)
	if len(listing.Entries) != 1 || listing.Entries[0].Prompt != "two" {
		t.Fatalf("got %+v", listing.Entries)
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	e, err := Create(config.ScopeProject, dir, "hello", "say hello")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := Delete(e); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(e.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected file to be removed: %v", err)
	}
}

func TestSetEnabled(t *testing.T) {
	dir := t.TempDir()
	e, err := Create(config.ScopeProject, dir, "hello", "say hello")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	disabled, err := SetEnabled(e, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.Enabled {
		t.Fatal("expected disabled entry")
	}
	if filepath.Base(disabled.Path)[0] != '_' {
		t.Fatalf("expected leading underscore in %q", disabled.Path)
	}
	re, err := SetEnabled(disabled, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !re.Enabled {
		t.Fatal("expected re-enabled entry")
	}
}

func TestMergeProjectOverridesGlobal(t *testing.T) {
	global := []Entry{
		{Name: "a", Prompt: "global a"},
		{Name: "b", Prompt: "global b"},
	}
	project := []Entry{
		{Name: "b", Prompt: "project b"},
		{Name: "c", Prompt: "project c"},
	}
	merged := Merge(global, project)
	if len(merged) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(merged))
	}
	byName := map[string]string{}
	for _, e := range merged {
		byName[e.Name] = e.Prompt
	}
	if byName["a"] != "global a" || byName["b"] != "project b" || byName["c"] != "project c" {
		t.Fatalf("got %+v", byName)
	}
}

func TestFilterCaseInsensitive(t *testing.T) {
	entries := []Entry{
		{Name: "Deploy", Prompt: "how to deploy"},
		{Name: "Test", Prompt: "run unit tests"},
		{Name: "deploy-prod", Prompt: "ship it"},
	}
	results := Filter(entries, "deploy")
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Name != "Deploy" {
		t.Fatalf("first result = %q", results[0].Name)
	}
}

func TestFilterEmptyQueryReturnsAll(t *testing.T) {
	entries := []Entry{
		{Name: "a", Prompt: "a"},
		{Name: "b", Prompt: "b"},
	}
	results := Filter(entries, "")
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

func TestSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Deploy App", "deploy-app"},
		{"_underscored! name ", "underscored-name"},
		{"123", "123"},
	}
	for _, c := range cases {
		got, err := Slug(c.in)
		if err != nil {
			t.Fatalf("Slug(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseFileName(t *testing.T) {
	order, slug, enabled, ok := ParseFileName("010-hello-world.md")
	if !ok || order != 10 || slug != "hello-world" || !enabled {
		t.Fatalf("got %d/%q/%v/%v", order, slug, enabled, ok)
	}
	order, slug, enabled, ok = ParseFileName("_010-hello-world.md")
	if !ok || enabled {
		t.Fatalf("expected disabled entry")
	}
	if _, _, _, ok := ParseFileName("bad.md"); ok {
		t.Fatal("expected bad.md to fail")
	}
}
