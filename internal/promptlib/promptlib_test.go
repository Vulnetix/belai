package promptlib

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestDirResolvesScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	workdir := t.TempDir()

	if got, err := Dir(config.ScopeProject, workdir); err != nil || got != filepath.Join(workdir, ".vulnetix", "prompts") {
		t.Fatalf("Dir(project) = %q, %v", got, err)
	}
	if got, err := Dir(config.ScopeGlobal, workdir); err != nil || got != filepath.Join(home, "prompts") {
		t.Fatalf("Dir(global) = %q, %v", got, err)
	}
	if _, err := Dir(config.Scope("bogus"), workdir); err == nil {
		t.Fatal("expected an error for an unknown scope")
	}
}

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
	if e.Order != 10 {
		t.Fatalf("first order = %d, want 10", e.Order)
	}

	listing, err := Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Prompt != "say hello" {
		t.Fatalf("got %+v", listing.Entries)
	}
}

func TestCreateAppendsAtLastPlusTen(t *testing.T) {
	dir := t.TempDir()
	a, err := Create(config.ScopeProject, dir, "a", "a")
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := Create(config.ScopeProject, dir, "b", "b")
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if a.Order != 10 || b.Order != 20 {
		t.Fatalf("orders = %d, %d, want 10, 20", a.Order, b.Order)
	}
	listing, _ := Load(config.ScopeProject, dir)
	if len(listing.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(listing.Entries))
	}
	if listing.Entries[0].Name != "a" || listing.Entries[1].Name != "b" {
		t.Fatalf("order = %+v", listing.Entries)
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
	// The first body survives: Create never upserts.
	listing, _ := Load(config.ScopeProject, dir)
	if len(listing.Entries) != 1 || listing.Entries[0].Prompt != "one" {
		t.Fatalf("got %+v, want the original body intact", listing.Entries)
	}
}

func TestCreateLibraryFull(t *testing.T) {
	// A scope with 999 entries cannot fit another; the filename grammar only
	// addresses 001..999. Fill the directory directly rather than through
	// Create, which would need 999 round trips.
	dir := t.TempDir()
	pd := promptsDir(t, dir)
	for i := 1; i <= 999; i++ {
		name := fmt.Sprintf("e%d", i)
		if err := writePrompt(filepath.Join(pd, FileName(i, name, true)), "x", 0o644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := Create(config.ScopeProject, dir, "new", "x"); !errors.Is(err, ErrLibraryFull) {
		t.Fatalf("expected ErrLibraryFull, got %v", err)
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

func TestUpdatePreservesPathOrderEnabled(t *testing.T) {
	dir := t.TempDir()
	e, _ := Create(config.ScopeProject, dir, "hello", "one")
	e, _ = SetEnabled(e, false)
	oldPath := e.Path
	oldOrder := e.Order

	updated, err := Update(e, "two")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Path != oldPath {
		t.Fatalf("path = %q, want %q", updated.Path, oldPath)
	}
	if updated.Order != oldOrder || updated.Enabled {
		t.Fatalf("order/enabled changed: %d/%v", updated.Order, updated.Enabled)
	}
	data, err := os.ReadFile(updated.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "two\n" {
		t.Fatalf("file = %q, want %q", string(data), "two\n")
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

func TestSetEnabledPreservesBodyByteForByte(t *testing.T) {
	dir := t.TempDir()
	body := "line one\nline two"
	e, _ := Create(config.ScopeProject, dir, "hello", body)
	before, err := os.ReadFile(e.Path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	d, err := SetEnabled(e, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	after, err := os.ReadFile(d.Path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("body changed across rename: %q != %q", before, after)
	}
	re, _ := SetEnabled(d, true)
	after2, _ := os.ReadFile(re.Path)
	if string(after) != string(after2) {
		t.Fatalf("body changed across re-enable: %q != %q", after, after2)
	}
}

func TestStraysCollectedNotLoaded(t *testing.T) {
	dir := t.TempDir()
	pd := promptsDir(t, dir)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(pd, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("010-good.md", "good")
	write("README.md", "readme")
	write("deploy.md", "no order")
	write("010-good.md.swp", "swap")
	if err := os.Mkdir(filepath.Join(pd, "010-sub.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	listing, err := Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "good" {
		t.Fatalf("entries = %+v, want only 010-good", listing.Entries)
	}
	wantStrays := []string{"010-good.md.swp", "010-sub.md", "README.md", "deploy.md"}
	if len(listing.Strays) != len(wantStrays) {
		t.Fatalf("strays = %v, want %v", listing.Strays, wantStrays)
	}
	for i := range wantStrays {
		if listing.Strays[i] != wantStrays[i] {
			t.Fatalf("strays = %v, want %v", listing.Strays, wantStrays)
		}
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
	order := []string{}
	for _, e := range merged {
		order = append(order, e.Name+":"+e.Prompt)
	}
	want := []string{"a:global a", "b:project b", "c:project c"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestMergeKeepsGlobalSlot(t *testing.T) {
	global := []Entry{
		{Name: "a", Order: 10, Prompt: "global a", Scope: config.ScopeGlobal},
		{Name: "b", Order: 20, Prompt: "global b", Scope: config.ScopeGlobal},
	}
	project := []Entry{
		{Name: "b", Order: 50, Prompt: "project b", Scope: config.ScopeProject},
	}
	merged := Merge(global, project)
	if len(merged) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(merged))
	}
	if merged[0].Name != "a" || merged[1].Name != "b" {
		t.Fatalf("names out of global order: %v", merged)
	}
	if merged[1].Prompt != "project b" {
		t.Fatalf("override content lost: %+v", merged[1])
	}
	if merged[1].Scope != config.ScopeProject {
		t.Fatalf("override must keep its project identity: %+v", merged[1])
	}
}

func TestDisabledProjectEntryVetoesGlobal(t *testing.T) {
	global := []Entry{{Name: "deploy", Prompt: "global", Enabled: true, Scope: config.ScopeGlobal}}
	project := []Entry{{Name: "deploy", Prompt: "project", Enabled: false, Scope: config.ScopeProject}}
	merged := Merge(global, project)
	if len(merged) != 1 || merged[0].Prompt != "project" || merged[0].Enabled {
		t.Fatalf("merge = %+v, want the disabled project entry", merged)
	}
	if got := Enabled(merged); len(got) != 0 {
		t.Fatalf("Enabled() = %+v, want empty (project veto)", got)
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
		{"a_b_c", "a-b-c"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"already-slugged", "already-slugged"},
		{"multi   spaces", "multi-spaces"},
	}
	for _, c := range cases {
		got, err := Slug(c.in)
		if err != nil {
			t.Fatalf("Slug(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Slug(%q) = %q, want %q", c.in, got, c.want)
		}
		if stringsContainsRune(got, '_') {
			t.Fatalf("Slug(%q) produced an underscore: %q", c.in, got)
		}
	}
}

func TestSlugEmptyAndTooLong(t *testing.T) {
	if _, err := Slug("!!!__"); err == nil {
		t.Fatal("expected error for a name with no alnum runes")
	}
	long := ""
	for i := 0; i < 80; i++ {
		long += "a"
	}
	if _, err := Slug(long); err == nil {
		t.Fatal("expected error for a slug longer than 64 runes")
	}
}

func TestParseFileNameAcceptReject(t *testing.T) {
	accept := []struct {
		base    string
		order   int
		slug    string
		enabled bool
	}{
		{"010-deploy.md", 10, "deploy", true},
		{"001-a.md", 1, "a", true},
		{"999-z.md", 999, "z", true},
		{"_030-old.md", 30, "old", false},
		{"010-deploy-app.md", 10, "deploy-app", true},
	}
	for _, c := range accept {
		order, slug, enabled, ok := ParseFileName(c.base)
		if !ok || order != c.order || slug != c.slug || enabled != c.enabled {
			t.Fatalf("ParseFileName(%q) = %d/%q/%v/%v, want %d/%q/%v/true", c.base, order, slug, enabled, ok, c.order, c.slug, c.enabled)
		}
	}
	reject := []string{
		"deploy.md", "10-deploy.md", "010_deploy.md", "010-Deploy.md",
		"010-deploy.txt", "000-deploy.md", "1000-deploy.md", "010-.md",
		"010-deploy-.md", "010--deploy.md", "010-deploy.md.bak",
	}
	for _, base := range reject {
		if _, _, _, ok := ParseFileName(base); ok {
			t.Fatalf("ParseFileName(%q) should fail", base)
		}
	}
}

func TestReorderRenumbersAndLeavesNoTemps(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(config.ScopeProject, dir, "a", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(config.ScopeProject, dir, "b", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(config.ScopeProject, dir, "c", "c"); err != nil {
		t.Fatal(err)
	}
	listing, _ := Load(config.ScopeProject, dir)

	moved, err := Reorder(config.ScopeProject, dir, listing.Entries, 0, 2)
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	want := []struct {
		name  string
		order int
	}{{"b", 10}, {"c", 20}, {"a", 30}}
	if len(moved) != len(want) {
		t.Fatalf("moved = %+v", moved)
	}
	for i := range want {
		if moved[i].Name != want[i].name || moved[i].Order != want[i].order {
			t.Fatalf("moved[%d] = %+v, want %+v", i, moved[i], want[i])
		}
	}

	// On disk the renumber holds and the old names are gone.
	reloaded, _ := Load(config.ScopeProject, dir)
	if len(reloaded.Entries) != 3 {
		t.Fatalf("reloaded %d entries, want 3", len(reloaded.Entries))
	}
	for i := range want {
		if reloaded.Entries[i].Name != want[i].name || reloaded.Entries[i].Order != want[i].order {
			t.Fatalf("reloaded[%d] = %+v, want %+v", i, reloaded.Entries[i], want[i])
		}
	}
	if _, err := os.Stat(filepath.Join(promptsDir(t, dir), "010-a.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old 010-a.md should be gone")
	}
	if temps, _ := filepath.Glob(filepath.Join(promptsDir(t, dir), ".belai-tmp-*")); len(temps) != 0 {
		t.Fatalf("temps left behind: %v", temps)
	}
}

func TestReorderAbortsOnCollisionLeavingDirectoryUnchanged(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(config.ScopeProject, dir, "a", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(config.ScopeProject, dir, "b", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(config.ScopeProject, dir, "c", "c"); err != nil {
		t.Fatal(err)
	}
	listing, _ := Load(config.ScopeProject, dir)
	pd := promptsDir(t, dir)

	// A directory squats on a final target name (020-c.md) that is not a
	// current source; it reads as a stray and must abort the reorder.
	collision := filepath.Join(pd, "020-c.md")
	if err := os.Mkdir(collision, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Reorder(config.ScopeProject, dir, listing.Entries, 0, 2); err == nil {
		t.Fatal("expected a collision error")
	}
	for _, name := range []string{"010-a.md", "020-b.md", "030-c.md"} {
		if _, err := os.Stat(filepath.Join(pd, name)); err != nil {
			t.Fatalf("%s should be untouched: %v", name, err)
		}
	}
	if temps, _ := filepath.Glob(filepath.Join(pd, ".belai-tmp-*")); len(temps) != 0 {
		t.Fatalf("temps left behind after abort: %v", temps)
	}
}

func TestCreateFileModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	workdir := t.TempDir()

	ge, err := Create(config.ScopeGlobal, workdir, "g", "global")
	if err != nil {
		t.Fatalf("create global: %v", err)
	}
	pe, err := Create(config.ScopeProject, workdir, "p", "project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	gi, err := os.Stat(ge.Path)
	if err != nil {
		t.Fatal(err)
	}
	pi, err := os.Stat(pe.Path)
	if err != nil {
		t.Fatal(err)
	}
	if gi.Mode().Perm() != 0o600 {
		t.Fatalf("global mode = %o, want 600", gi.Mode().Perm())
	}
	if pi.Mode().Perm() != 0o644 {
		t.Fatalf("project mode = %o, want 644", pi.Mode().Perm())
	}
}

func TestAtomicWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(config.ScopeProject, dir, "x", "x"); err != nil {
		t.Fatal(err)
	}
	if temps, _ := filepath.Glob(filepath.Join(promptsDir(t, dir), "*.tmp")); len(temps) != 0 {
		t.Fatalf("atomic temp left behind: %v", temps)
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

func stringsContainsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

// promptsDir returns the project prompt directory for a workdir, created.
func promptsDir(t *testing.T, workdir string) string {
	t.Helper()
	d := filepath.Join(workdir, ".vulnetix", "prompts")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}
