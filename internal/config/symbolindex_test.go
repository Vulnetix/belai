package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSymbolIndexDefaultsOnWithBoundedNumbers(t *testing.T) {
	var s Settings
	if !s.SymbolIndexEnabled() {
		t.Fatal("the symbol index must default on")
	}
	if s.SymbolIndexMaxFiles() != 2000 || s.SymbolIndexMaxResults() != 50 || s.SymbolIndexRefreshSecs() != 60 {
		t.Fatalf("defaults = %d %d %d", s.SymbolIndexMaxFiles(), s.SymbolIndexMaxResults(), s.SymbolIndexRefreshSecs())
	}
	no := false
	if (Settings{SymbolIndex: &SymbolIndexSettings{Enabled: &no}}).SymbolIndexEnabled() {
		t.Fatal("false must be off")
	}
	bad := Settings{SymbolIndex: &SymbolIndexSettings{MaxFiles: ip(5), MaxResults: ip(0), RefreshSecs: ip(99999)}}
	if bad.SymbolIndexMaxFiles() != 2000 || bad.SymbolIndexMaxResults() != 50 || bad.SymbolIndexRefreshSecs() != 60 {
		t.Fatal("an out-of-range number reached an accessor")
	}
}

func TestValidateSymbolIndexNamesTheKey(t *testing.T) {
	for key, c := range map[string]SymbolIndexSettings{
		"symbol_index.max_files":    {MaxFiles: ip(99)},
		"symbol_index.max_results":  {MaxResults: ip(201)},
		"symbol_index.refresh_secs": {RefreshSecs: ip(4)},
	} {
		c := c
		err := ValidateSymbolIndex(Settings{SymbolIndex: &c})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	ok := SymbolIndexSettings{MaxFiles: ip(10000), MaxResults: ip(200), RefreshSecs: ip(3600)}
	if err := ValidateSymbolIndex(Settings{SymbolIndex: &ok}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSymbolIndex(Settings{}); err != nil {
		t.Fatal(err)
	}
}

// A repository may switch the index off and do nothing else, so it cannot make
// a host index or return more.
func TestSymbolIndexProjectLayerCanOnlySwitchOff(t *testing.T) {
	yes, no := true, false
	user := Settings{SymbolIndex: &SymbolIndexSettings{MaxFiles: ip(500), MaxResults: ip(20), RefreshSecs: ip(30)}}

	got := user.Override(Settings{SymbolIndex: &SymbolIndexSettings{Enabled: &no}})
	if got.SymbolIndexEnabled() {
		t.Fatal("a project layer could not switch the index off")
	}
	if got.SymbolIndexMaxFiles() != 500 || got.SymbolIndexMaxResults() != 20 || got.SymbolIndexRefreshSecs() != 30 {
		t.Fatal("switching off changed the user's numbers")
	}
	off := Settings{SymbolIndex: &SymbolIndexSettings{Enabled: &no}}
	if off.Override(Settings{SymbolIndex: &SymbolIndexSettings{Enabled: &yes}}).SymbolIndexEnabled() {
		t.Fatal("a project layer turned back on what the user turned off")
	}
	got = user.Override(Settings{SymbolIndex: &SymbolIndexSettings{MaxFiles: ip(10000), MaxResults: ip(200), RefreshSecs: ip(5)}})
	if got.SymbolIndexMaxFiles() != 500 || got.SymbolIndexMaxResults() != 20 || got.SymbolIndexRefreshSecs() != 30 {
		t.Fatalf("a project layer changed a number: %+v", got.SymbolIndex)
	}
	got = (Settings{}).Override(Settings{SymbolIndex: &SymbolIndexSettings{MaxFiles: ip(10000), MaxResults: ip(200)}})
	if got.SymbolIndexMaxFiles() != 2000 || got.SymbolIndexMaxResults() != 50 || got.SymbolIndex != nil {
		t.Fatalf("a project layer raised a number: %+v", got.SymbolIndex)
	}
}

func TestSymbolIndexUserLayersSetAnyKeyInRange(t *testing.T) {
	eff := Effective{Origin: map[string]Source{}}
	eff.apply(Settings{SymbolIndex: &SymbolIndexSettings{MaxFiles: ip(10000), MaxResults: ip(200), RefreshSecs: ip(3600)}}, SourceGlobal)
	s := eff.Settings
	if s.SymbolIndexMaxFiles() != 10000 || s.SymbolIndexMaxResults() != 200 || s.SymbolIndexRefreshSecs() != 3600 {
		t.Fatalf("user layer lost: %+v", s.SymbolIndex)
	}
	if eff.Origin["symbol_index"] != SourceGlobal {
		t.Fatalf("origin = %q", eff.Origin["symbol_index"])
	}
	eff.apply(Settings{SymbolIndex: &SymbolIndexSettings{MaxFiles: ip(5)}}, SourceGlobal)
	if eff.Settings.SymbolIndexMaxFiles() != 10000 {
		t.Fatal("an out-of-range value replaced a good one")
	}
	no := false
	eff.apply(Settings{SymbolIndex: &SymbolIndexSettings{MaxResults: ip(1), Enabled: &no}}, SourceProject)
	if eff.Settings.SymbolIndexMaxResults() != 200 || eff.Settings.SymbolIndexEnabled() {
		t.Fatalf("project layer: %+v", eff.Settings.SymbolIndex)
	}
}

func TestResolveDropsProjectSymbolIndexNumbersWithANote(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveProject(workdir, Settings{SymbolIndex: &SymbolIndexSettings{MaxFiles: ip(10000), RefreshSecs: ip(5)}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.SymbolIndexMaxFiles() != 2000 || eff.Settings.SymbolIndexRefreshSecs() != 60 {
		t.Fatal("a project changed an index number")
	}
	if !hasNote(eff.Notes, "project symbol_index.max_files, symbol_index.refresh_secs ignored") {
		t.Fatalf("no drop note: %v", eff.Notes)
	}
}

func TestProjectSymbolIndexPathIsPerWorkdirInTheStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	a, err := ProjectSymbolIndexPath("/work/a")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ProjectSymbolIndexPath("/work/b")
	if a == b {
		t.Fatal("two working directories share a cache")
	}
	dir, _ := SymbolIndexDir()
	if filepath.Dir(a) != dir || !strings.HasPrefix(a, home) || !strings.HasSuffix(a, ".idx") {
		t.Fatalf("path = %q, dir %q, home %q", a, dir, home)
	}
}
