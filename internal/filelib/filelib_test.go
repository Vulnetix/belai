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
