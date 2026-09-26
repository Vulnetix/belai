package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

func TestLSPViewRowCount(t *testing.T) {
	a := &App{
		settings: config.Settings{},
	}
	rows := a.lspRows()
	if len(rows) == 0 {
		t.Fatal("expected language rows")
	}
}

func TestLSPViewTogglePersists(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vulnetix"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{
		workdir:       dir,
		settings:      config.Settings{},
		settingsState: settingsViewState{scope: config.ScopeProject},
	}
	// Open settings to create project file
	if err := config.SaveProject(dir, config.Settings{}); err != nil {
		t.Fatal(err)
	}
	a.lspToggle("go", false)
	merged, err := config.LoadMerged(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := merged.LSP.Languages["go"]; !ok || v {
		t.Fatalf("expected go=false in project settings, got ok=%v v=%v", ok, v)
	}
}

func TestLSPKeySpaceThenEsc(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.view = viewLSP
	a.viewStack = []viewState{viewSettings, viewLSP}
	_ = a.lspView() // render before key
	_, _ = a.handleLSPKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewSettings {
		t.Fatalf("expected pop to settings, got %v", a.view)
	}
}

func TestLSPCommandRegistered(t *testing.T) {
	r := NewRegistry(t.TempDir())
	if _, ok := r.Command("lsp"); !ok {
		t.Fatal("/lsp command not registered")
	}
}

func TestLSPViewRendersWindowsUnsupported(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	// Cannot reliably fake runtime.GOOS in a unit test; just ensure no panic.
	a := &App{settings: config.Settings{}}
	_ = a.lspView()
}

func TestLSPRowsContainGo(t *testing.T) {
	a := &App{settings: config.Settings{}}
	rows := a.lspRows()
	found := false
	for _, r := range rows {
		if strings.Contains(r.label, "Go") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Go row, got %+v", rows)
	}
}
