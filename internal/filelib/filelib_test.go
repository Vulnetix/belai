package filelib

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func specs() map[string]Spec {
	return map[string]Spec{
		"prompt": {
			Ext:        ".md",
			TempPrefix: ".tmp-prompt-",
			GlobalDir:  config.GlobalPromptsDir,
			ProjectDir: config.ProjectPromptsDir,
		},
		"process": {
			Ext:        ".sh",
			TempPrefix: ".tmp-process-",
			GlobalDir:  config.GlobalProcessesDir,
			ProjectDir: config.ProjectProcessesDir,
		},
	}
}

func libDir(t *testing.T, s Spec, workdir string) string {
	t.Helper()
	switch s.Ext {
	case ".md":
		return filepath.Join(workdir, ".vulnetix", "prompts")
	case ".sh":
		return filepath.Join(workdir, ".vulnetix", "processes")
	default:
		t.Fatalf("unknown spec extension %q", s.Ext)
		return ""
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			listing, err := s.Load(config.ScopeProject, t.TempDir())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(listing.Entries) != 0 {
				t.Fatalf("expected empty library, got %d entries", len(listing.Entries))
			}
		})
	}
}

func TestCreateAndLoad(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			e, err := s.Create(config.ScopeProject, dir, "hello", "say hello")
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

			listing, err := s.Load(config.ScopeProject, dir)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(listing.Entries) != 1 || listing.Entries[0].Body != "say hello" {
				t.Fatalf("got %+v", listing.Entries)
			}
		})
	}
}

func TestCreateAppendsAtLastPlusTen(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			a, err := s.Create(config.ScopeProject, dir, "a", "a")
			if err != nil {
				t.Fatalf("create a: %v", err)
			}
			b, err := s.Create(config.ScopeProject, dir, "b", "b")
			if err != nil {
				t.Fatalf("create b: %v", err)
			}
			if a.Order != 10 || b.Order != 20 {
				t.Fatalf("orders = %d, %d, want 10, 20", a.Order, b.Order)
			}
		})
	}
}

func TestCreateDuplicateReturnsErrNameExists(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := s.Create(config.ScopeProject, dir, "hello", "one"); err != nil {
				t.Fatalf("first create: %v", err)
			}
			_, err := s.Create(config.ScopeProject, dir, "hello", "two")
			if !errors.Is(err, ErrNameExists) {
				t.Fatalf("expected ErrNameExists, got %v", err)
			}
			listing, _ := s.Load(config.ScopeProject, dir)
			if len(listing.Entries) != 1 || listing.Entries[0].Body != "one" {
				t.Fatalf("got %+v, want the original body intact", listing.Entries)
			}
		})
	}
}

func TestCreateLibraryFull(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			d := libDir(t, s, dir)
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 999; i++ {
				name := fmt.Sprintf("e%d", i)
				if err := os.WriteFile(filepath.Join(d, s.FileName(i, name, true)), []byte("x\n"), 0o644); err != nil {
					t.Fatalf("write %d: %v", i, err)
				}
			}
			if _, err := s.Create(config.ScopeProject, dir, "new", "x"); !errors.Is(err, ErrLibraryFull) {
				t.Fatalf("expected ErrLibraryFull, got %v", err)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			e, err := s.Create(config.ScopeProject, dir, "hello", "one")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			updated, err := s.Update(e, "two")
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if updated.Body != "two" {
				t.Fatalf("body = %q, want two", updated.Body)
			}
		})
	}
}

func TestUpdatePreservesPathOrderEnabled(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			e, _ := s.Create(config.ScopeProject, dir, "hello", "one")
			e, _ = s.SetEnabled(e, false)
			oldPath := e.Path
			oldOrder := e.Order

			updated, err := s.Update(e, "two")
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
		})
	}
}

func TestDelete(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			e, err := s.Create(config.ScopeProject, dir, "hello", "say hello")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := s.Delete(e); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := os.Stat(e.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected file to be removed: %v", err)
			}
		})
	}
}

func TestSetEnabled(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			e, err := s.Create(config.ScopeProject, dir, "hello", "say hello")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			disabled, err := s.SetEnabled(e, false)
			if err != nil {
				t.Fatalf("disable: %v", err)
			}
			if disabled.Enabled {
				t.Fatal("expected disabled entry")
			}
			re, err := s.SetEnabled(disabled, true)
			if err != nil {
				t.Fatalf("enable: %v", err)
			}
			if !re.Enabled {
				t.Fatal("expected re-enabled entry")
			}
		})
	}
}

func TestSetEnabledPreservesBodyByteForByte(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			body := "line one\nline two"
			e, _ := s.Create(config.ScopeProject, dir, "hello", body)
			before, _ := os.ReadFile(e.Path)
			d, _ := s.SetEnabled(e, false)
			after, _ := os.ReadFile(d.Path)
			if string(before) != string(after) {
				t.Fatalf("body changed across rename: %q != %q", before, after)
			}
		})
	}
}

func TestStraysCollectedNotLoaded(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			d := libDir(t, s, dir)
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			write := func(name, content string) {
				if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write(s.FileName(10, "good", true), "good")
			write("README"+s.Ext, "readme")
			write("no-order"+s.Ext, "no order")
			write(s.FileName(10, "good", true)+".swp", "swap")
			if err := os.Mkdir(filepath.Join(d, "010-sub"+s.Ext), 0o755); err != nil {
				t.Fatal(err)
			}

			listing, err := s.Load(config.ScopeProject, dir)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(listing.Entries) != 1 || listing.Entries[0].Name != "good" {
				t.Fatalf("entries = %+v, want only good", listing.Entries)
			}
			if len(listing.Strays) != 4 {
				t.Fatalf("strays = %v, want 4", listing.Strays)
			}
		})
	}
}

func TestMergeProjectOverridesGlobal(t *testing.T) {
	global := []Entry{
		{Name: "a", Body: "global a"},
		{Name: "b", Body: "global b"},
	}
	project := []Entry{
		{Name: "b", Body: "project b"},
		{Name: "c", Body: "project c"},
	}
	merged := Merge(global, project)
	if len(merged) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(merged))
	}
	want := []string{"a:global a", "b:project b", "c:project c"}
	for i, w := range want {
		if merged[i].Name+":"+merged[i].Body != w {
			t.Fatalf("merged[%d] = %s:%s, want %s", i, merged[i].Name, merged[i].Body, w)
		}
	}
}

func TestMergeKeepsGlobalSlot(t *testing.T) {
	global := []Entry{
		{Name: "a", Order: 10, Body: "global a", Scope: config.ScopeGlobal},
		{Name: "b", Order: 20, Body: "global b", Scope: config.ScopeGlobal},
	}
	project := []Entry{
		{Name: "b", Order: 50, Body: "project b", Scope: config.ScopeProject},
	}
	merged := Merge(global, project)
	if len(merged) != 2 || merged[0].Name != "a" || merged[1].Name != "b" {
		t.Fatalf("names out of global order: %v", merged)
	}
	if merged[1].Body != "project b" {
		t.Fatalf("override content lost: %+v", merged[1])
	}
	if merged[1].Scope != config.ScopeProject {
		t.Fatalf("override must keep project identity: %+v", merged[1])
	}
}

func TestDisabledProjectEntryVetoesGlobal(t *testing.T) {
	global := []Entry{{Name: "deploy", Body: "global", Enabled: true, Scope: config.ScopeGlobal}}
	project := []Entry{{Name: "deploy", Body: "project", Enabled: false, Scope: config.ScopeProject}}
	merged := Merge(global, project)
	if len(merged) != 1 || merged[0].Body != "project" || merged[0].Enabled {
		t.Fatalf("merge = %+v, want the disabled project entry", merged)
	}
	if got := Enabled(merged); len(got) != 0 {
		t.Fatalf("Enabled() = %+v, want empty (project veto)", got)
	}
}

func TestFilterCaseInsensitive(t *testing.T) {
	entries := []Entry{
		{Name: "Deploy", Body: "how to deploy"},
		{Name: "Test", Body: "run unit tests"},
		{Name: "deploy-prod", Body: "ship it"},
	}
	results := Filter(entries, "deploy")
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestFilterEmptyQueryReturnsAll(t *testing.T) {
	entries := []Entry{{Name: "a", Body: "a"}, {Name: "b", Body: "b"}}
	results := Filter(entries, "")
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestParseFileNameAcceptReject(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			accept := []struct {
				base    string
				order   int
				slug    string
				enabled bool
			}{
				{s.FileName(10, "deploy", true), 10, "deploy", true},
				{s.FileName(1, "a", true), 1, "a", true},
				{s.FileName(999, "z", true), 999, "z", true},
				{s.FileName(30, "old", false), 30, "old", false},
				{s.FileName(10, "deploy-app", true), 10, "deploy-app", true},
			}
			for _, c := range accept {
				order, slug, enabled, ok := s.ParseFileName(c.base)
				if !ok || order != c.order || slug != c.slug || enabled != c.enabled {
					t.Fatalf("ParseFileName(%q) = %d/%q/%v, want %d/%q/%v/true", c.base, order, slug, enabled, c.order, c.slug, c.enabled)
				}
			}
			reject := []string{
				"deploy" + s.Ext,
				"10-deploy" + s.Ext,
				"010_deploy" + s.Ext,
				"010-Deploy" + s.Ext,
				"010-deploy.txt",
				s.FileName(0, "deploy", true),
				"1000-deploy" + s.Ext,
				"010-" + s.Ext,
				"010-deploy-" + s.Ext,
				"010--deploy" + s.Ext,
				"010-deploy" + s.Ext + ".bak",
			}
			for _, base := range reject {
				if _, _, _, ok := s.ParseFileName(base); ok {
					t.Fatalf("ParseFileName(%q) should fail", base)
				}
			}
		})
	}
}

func TestReorderRenumbersAndLeavesNoTemps(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := s.Create(config.ScopeProject, dir, "a", "a"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(config.ScopeProject, dir, "b", "b"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(config.ScopeProject, dir, "c", "c"); err != nil {
				t.Fatal(err)
			}
			listing, _ := s.Load(config.ScopeProject, dir)

			moved, err := s.Reorder(config.ScopeProject, dir, listing.Entries, 0, 2)
			if err != nil {
				t.Fatalf("reorder: %v", err)
			}
			want := []struct {
				name  string
				order int
			}{
				{"b", 10},
				{"c", 20},
				{"a", 30},
			}
			if len(moved) != len(want) {
				t.Fatalf("moved = %+v", moved)
			}
			for i, w := range want {
				if moved[i].Name != w.name || moved[i].Order != w.order {
					t.Fatalf("moved[%d] = %+v, want %+v", i, moved[i], w)
				}
			}

			reloaded, _ := s.Load(config.ScopeProject, dir)
			if len(reloaded.Entries) != 3 {
				t.Fatalf("reloaded %d entries, want 3", len(reloaded.Entries))
			}
			for i, w := range want {
				if reloaded.Entries[i].Name != w.name || reloaded.Entries[i].Order != w.order {
					t.Fatalf("reloaded[%d] = %+v, want %+v", i, reloaded.Entries[i], w)
				}
			}
			d := libDir(t, s, dir)
			if _, err := os.Stat(filepath.Join(d, s.FileName(10, "a", true))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("old first file should be gone")
			}
			if temps, _ := filepath.Glob(filepath.Join(d, ".belai-tmp-*")); len(temps) != 0 {
				t.Fatalf("temps left behind: %v", temps)
			}
		})
	}
}

func TestCreateFileModes(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("BELAI_HOME", home)
			workdir := t.TempDir()

			ge, err := s.Create(config.ScopeGlobal, workdir, "g", "global")
			if err != nil {
				t.Fatalf("create global: %v", err)
			}
			pe, err := s.Create(config.ScopeProject, workdir, "p", "project")
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
		})
	}
}

func TestAtomicWriteLeavesNoTemp(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := s.Create(config.ScopeProject, dir, "x", "x"); err != nil {
				t.Fatal(err)
			}
			d := libDir(t, s, dir)
			if temps, _ := filepath.Glob(filepath.Join(d, "*.tmp")); len(temps) != 0 {
				t.Fatalf("atomic temp left behind: %v", temps)
			}
		})
	}
}

func TestMatchAgainstBody(t *testing.T) {
	e := Entry{Name: "x", Body: "deploy to production"}
	if !Match(e, "production") {
		t.Fatalf("expected match against body text")
	}
	if Match(e, "staging") {
		t.Fatalf("expected no match")
	}
}

func TestPut(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			load := func() []Entry {
				l, err := s.Load(config.ScopeProject, dir)
				if err != nil {
					t.Fatal(err)
				}
				return l.Entries
			}
			// A new entry with no order goes after the last, like Create.
			a, err := s.Put(config.ScopeProject, dir, "alpha", "one", 0, true)
			if err != nil || a.Order != 10 || !a.Enabled {
				t.Fatalf("new, no order: %+v, %v", a, err)
			}
			// A new disabled entry with no order is disabled at the next slot.
			b, err := s.Put(config.ScopeProject, dir, "beta", "two", 0, false)
			if err != nil || b.Order != 20 || b.Enabled {
				t.Fatalf("new disabled: %+v, %v", b, err)
			}
			// An explicit order is taken as given, even if it shares a number.
			c, err := s.Put(config.ScopeProject, dir, "gamma", "three", 10, true)
			if err != nil || c.Order != 10 {
				t.Fatalf("explicit order: %+v, %v", c, err)
			}
			// A replace with order 0 keeps the entry's order and its place.
			a2, err := s.Put(config.ScopeProject, dir, "alpha", "ONE", 0, true)
			if err != nil || a2.Order != 10 || a2.Path != a.Path {
				t.Fatalf("replace keeps order: %+v (was %+v), %v", a2, a, err)
			}
			// A replace that changes order and enabled moves the file: one file per name.
			a3, err := s.Put(config.ScopeProject, dir, "alpha", "uno", 300, false)
			if err != nil || a3.Order != 300 || a3.Enabled || a3.Path == a.Path {
				t.Fatalf("replace moves: %+v, %v", a3, err)
			}
			if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
				t.Errorf("the replaced file is still there: %v", err)
			}
			var names []string
			for _, e := range load() {
				names = append(names, fmt.Sprintf("%s:%d:%v:%s", e.Name, e.Order, e.Enabled, e.Body))
			}
			want := []string{"gamma:10:true:three", "beta:20:false:two", "alpha:300:false:uno"}
			if fmt.Sprint(names) != fmt.Sprint(want) {
				t.Fatalf("library = %v, want %v", names, want)
			}
			// No temp file is left behind.
			des, _ := os.ReadDir(libDir(t, s, dir))
			for _, de := range des {
				if _, _, _, ok := s.ParseFileName(de.Name()); !ok {
					t.Errorf("stray %s after Put", de.Name())
				}
			}
		})
	}
}

func TestPutRefusesWhatItCannotPlace(t *testing.T) {
	s := specs()["prompt"]
	dir := t.TempDir()
	for _, name := range []string{"Upper", "a b", "a_b", ""} {
		if _, err := s.Put(config.ScopeProject, dir, name, "x", 0, true); err == nil {
			t.Errorf("name %q was put", name)
		}
	}
	for _, order := range []int{-1, 1000} {
		if _, err := s.Put(config.ScopeProject, dir, "ok", "x", order, true); err == nil {
			t.Errorf("order %d was put", order)
		}
	}
	d := filepath.Join(dir, ".vulnetix", "prompts")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 999; i++ {
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("%03d-p%d.md", i, i)), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(config.ScopeProject, dir, "extra", "x", 5, true); !errors.Is(err, ErrLibraryFull) {
		t.Errorf("a full library took another: %v", err)
	}
	if _, err := s.Put(config.ScopeProject, dir, "extra", "x", 0, true); !errors.Is(err, ErrLibraryFull) {
		t.Errorf("a full library took another at the end: %v", err)
	}
	// Replacing an entry of a full library is fine.
	if _, err := s.Put(config.ScopeProject, dir, "p5", "new", 0, true); err != nil {
		t.Errorf("replace in a full library: %v", err)
	}
}

// A spec with an alternate extension reads both and keeps each file's own
// extension through every operation.
func mixedSpec() Spec {
	return Spec{Ext: ".sh", AltExts: []string{".json"}, TempPrefix: ".tmp-process-", GlobalDir: config.GlobalProcessesDir, ProjectDir: config.ProjectProcessesDir}
}

func writeAt(t *testing.T, dir, name, body string) {
	t.Helper()
	d := filepath.Join(dir, ".vulnetix", "processes")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAltExtsAreEntries(t *testing.T) {
	s := mixedSpec()
	dir := t.TempDir()
	writeAt(t, dir, "010-legacy.sh", "echo hi\n")
	writeAt(t, dir, "020-structured.json", `{"name":"x"}`+"\n")
	writeAt(t, dir, "_030-off.json", "{}\n")
	writeAt(t, dir, "040-other.txt", "stray")
	l, err := s.Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range l.Entries {
		got = append(got, fmt.Sprintf("%s:%s:%v", e.Name, e.Ext, e.Enabled))
	}
	if fmt.Sprint(got) != "[legacy:.sh:true structured:.json:true off:.json:false]" || fmt.Sprint(l.Strays) != "[040-other.txt]" {
		t.Fatalf("entries %v strays %v", got, l.Strays)
	}
	if o, slug, en, ext, ok := s.ParseFileNameExt("_030-off.json"); !ok || o != 30 || slug != "off" || en || ext != ".json" {
		t.Errorf("ParseFileNameExt = %d %s %v %s %v", o, slug, en, ext, ok)
	}
	// The plain parser still answers for either extension.
	if _, _, _, ok := s.ParseFileName("010-a.json"); !ok {
		t.Error("ParseFileName rejects the alternate extension")
	}
	if _, _, _, ok := s.ParseFileName("010-a.md"); ok {
		t.Error("ParseFileName accepts an extension the spec does not read")
	}
}

func TestSameSlugUnderTwoExtensionsTheAlternateWins(t *testing.T) {
	s := mixedSpec()
	dir := t.TempDir()
	writeAt(t, dir, "010-web.sh", "old\n")
	writeAt(t, dir, "020-web.json", "{}\n")
	l, err := s.Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 1 || l.Entries[0].Ext != ".json" || fmt.Sprint(l.Strays) != "[010-web.sh]" {
		t.Fatalf("entries %+v strays %v", l.Entries, l.Strays)
	}
	// The stray is never touched.
	if _, err := os.Stat(filepath.Join(dir, ".vulnetix", "processes", "010-web.sh")); err != nil {
		t.Errorf("the stray was removed: %v", err)
	}
	// Create refuses the name, as it does for any slug in use.
	if _, err := s.Create(config.ScopeProject, dir, "web", "x"); !errors.Is(err, ErrNameExists) {
		t.Errorf("Create over a json entry: %v", err)
	}
}

func TestAltExtsSurviveToggleAndReorder(t *testing.T) {
	s := mixedSpec()
	dir := t.TempDir()
	writeAt(t, dir, "010-a.sh", "a\n")
	writeAt(t, dir, "020-b.json", "{}\n")
	writeAt(t, dir, "030-c.sh", "c\n")
	l, _ := s.Load(config.ScopeProject, dir)
	b, err := s.SetEnabled(l.Entries[1], false)
	if err != nil || filepath.Base(b.Path) != "_020-b.json" {
		t.Fatalf("SetEnabled: %+v %v", b, err)
	}
	l, _ = s.Load(config.ScopeProject, dir)
	moved, err := s.Reorder(config.ScopeProject, dir, l.Entries, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range moved {
		names = append(names, filepath.Base(e.Path))
	}
	if fmt.Sprint(names) != "[010-c.sh 020-a.sh _030-b.json]" {
		t.Fatalf("after reorder: %v", names)
	}
	des, _ := os.ReadDir(filepath.Join(dir, ".vulnetix", "processes"))
	if len(des) != 3 {
		t.Errorf("files after reorder: %d", len(des))
	}
}

func TestPutExt(t *testing.T) {
	s := mixedSpec()
	dir := t.TempDir()
	writeAt(t, dir, "010-web.sh", "old\n")
	// A structured entry replaces a legacy one of the same name and keeps its order.
	e, err := s.PutExt(config.ScopeProject, dir, "web", `{"name":"web"}`, 0, true, ".json")
	if err != nil || e.Order != 10 || e.Ext != ".json" || filepath.Base(e.Path) != "010-web.json" {
		t.Fatalf("replace: %+v %v", e, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".vulnetix", "processes", "010-web.sh")); !os.IsNotExist(err) {
		t.Errorf("the legacy file is still there: %v", err)
	}
	// A new alternate entry with no order goes last.
	e2, err := s.PutExt(config.ScopeProject, dir, "api", "{}", 0, false, ".json")
	if err != nil || e2.Order != 20 || filepath.Base(e2.Path) != "_020-api.json" {
		t.Fatalf("new: %+v %v", e2, err)
	}
	if _, err := s.PutExt(config.ScopeProject, dir, "x", "{}", 0, true, ".yaml"); err == nil {
		t.Error("an extension the spec does not read was put")
	}
	// Put keeps the primary extension.
	e3, err := s.Put(config.ScopeProject, dir, "legacy", "echo", 0, true)
	if err != nil || e3.Ext != ".sh" && e3.Ext != "" {
		t.Fatalf("Put: %+v %v", e3, err)
	}
}

// A reorder moves each file to the name of its own entry: the body travels with
// the entry, whichever position it takes.
func TestReorderKeepsEachBodyWithItsEntry(t *testing.T) {
	for name, s := range specs() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, n := range []string{"a", "b", "c", "d"} {
				if _, err := s.Create(config.ScopeProject, dir, n, "body of "+n); err != nil {
					t.Fatal(err)
				}
			}
			l, _ := s.Load(config.ScopeProject, dir)
			for _, mv := range [][2]int{{0, 3}, {3, 1}, {2, 0}} {
				l, _ = s.Load(config.ScopeProject, dir)
				if _, err := s.Reorder(config.ScopeProject, dir, l.Entries, mv[0], mv[1]); err != nil {
					t.Fatal(err)
				}
				after, _ := s.Load(config.ScopeProject, dir)
				if len(after.Entries) != 4 {
					t.Fatalf("%d entries after %v", len(after.Entries), mv)
				}
				for _, e := range after.Entries {
					if e.Body != "body of "+e.Name {
						t.Fatalf("after %v: %s holds %q", mv, e.Name, e.Body)
					}
				}
			}
		})
	}
}
