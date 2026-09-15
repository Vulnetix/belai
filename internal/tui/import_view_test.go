package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
)

func writeAgentFixture(t *testing.T, home, rel, content string) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestImportViewListsFindings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAgentFixture(t, home, ".pi/agent/auth.json", `{"openai":"sk-test"}`)

	a := New(Options{})
	a.importState.rows = a.importRows()
	view := a.importView()
	if !strings.Contains(view, "openai") {
		t.Fatalf("view should list openai: %q", view)
	}
}

func TestImportViewMasksSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAgentFixture(t, home, ".pi/agent/auth.json", `{"openai":"sk-supersecret"}`)

	a := New(Options{})
	a.importState.rows = a.importRows()
	view := a.importView()
	if strings.Contains(view, "sk-supersecret") {
		t.Fatalf("view leaked secret: %q", view)
	}
}

func TestImportViewRequiresOverwriteConfirm(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if err := resolver.Store("openai", "api_key", "existing-key", credentials.SourceUserFile); err != nil {
		t.Fatalf("Store: %v", err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAgentFixture(t, home, ".pi/agent/auth.json", `{"openai":"new-key"}`)

	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.importState.rows = a.importRows()
	idx := -1
	for i := range a.importState.rows {
		if a.importState.rows[i].found.Provider == "openai" {
			a.importState.rows[i].chosen = true
			a.importState.selected = i
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no openai finding")
	}
	if a.importState.rows[idx].existing == "" {
		t.Fatal("expected existing origin to be detected")
	}

	m, _ := a.handleImportKey(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if !a.importState.confirming {
		t.Fatal("expected overwrite confirm to be armed")
	}
}

func TestImportSavesProfileAndSecret(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAgentFixture(t, home, ".pi/agent/models.json",
		`{"providers":{"my-llm":{"api":"openai-completions","apiKey":"sk-test","baseUrl":"https://llm.example/v1"}}}`)

	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.importState = importViewState{
		backend: credentials.SourceUserFile,
		scope:   config.ScopeGlobal,
		rows:    a.importRows(),
	}
	idx := -1
	for i := range a.importState.rows {
		if a.importState.rows[i].found.Provider == "my-llm" {
			a.importState.rows[i].chosen = true
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no my-llm finding")
	}

	a.doImport()

	s, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if _, ok := s.Providers["my-llm"]; !ok {
		t.Fatalf("profile not written: %+v", s.Providers)
	}
	v, _, ok := resolver.Lookup("my-llm", "api_key")
	if !ok || v != "sk-test" {
		t.Fatalf("lookup = %q, %v; want sk-test", v, ok)
	}
}

func TestImportNotImportableRowCannotBeChosen(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAgentFixture(t, home, ".config/goose/secrets.yaml", "SOME_RANDOM: value\n")

	a := New(Options{})
	a.importState.rows = a.importRows()
	idx := -1
	for i := range a.importState.rows {
		if !a.importState.rows[i].found.Importable() {
			idx = i
			a.importState.selected = i
		}
	}
	if idx < 0 {
		t.Fatal("no non-importable row")
	}
	m, _ := a.handleImportKey(tea.KeyMsg{Type: tea.KeySpace})
	a = m.(*App)
	if a.importState.rows[idx].chosen {
		t.Fatal("non-importable row must not be toggleable")
	}
}

func TestImportEscReturnsToCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := New(Options{})
	a.push(viewCredentials)
	a.push(viewImport)
	m, _ := a.handleImportKey(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(*App)
	if a.view != viewCredentials {
		t.Fatalf("view = %v, want viewCredentials", a.view)
	}
}
