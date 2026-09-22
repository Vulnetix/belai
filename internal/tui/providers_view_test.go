package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/wire"
)

func TestProvidersViewEnterBuildsRows(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.push(viewProviders)
	if len(a.providersState.rows) == 0 {
		t.Fatal("expected non-empty provider rows after push(viewProviders)")
	}
}

func TestProvidersCommandBarePushesView(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	cmd, ok := a.registry.Command("providers")
	if !ok {
		t.Fatal("providers command not found")
	}
	_ = cmd.Run(a, "")
	if a.view != viewProviders {
		t.Fatalf("view = %v, want viewProviders", a.view)
	}
}

func TestProvidersReportCommandDoesNotPushView(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	cmd, _ := a.registry.Command("providers")
	_ = cmd.Run(a, "report")
	if a.view == viewProviders {
		t.Fatal("/providers report should not push the providers view")
	}
}

func TestProviderDetailCredentialsShowsBackend(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	a.openProviderDetail("openai")
	view := a.providerDetailView()
	if !strings.Contains(view, "storing to") {
		t.Fatal("expected active backend line")
	}
	if !strings.Contains(view, "backends") {
		t.Fatal("expected backends line")
	}
}

func TestProviderDetailServerLaunchOpensEditorAndParsesArgs(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	a.openProviderDetail("llama-server")
	a.providerDetailState.tab = providerTabServer

	m := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")}
	a.handleProviderDetailKey(m)
	if !a.providerDetailState.repoMode {
		t.Fatal("expected repo editor mode after l")
	}
	if a.providerDetailState.repoAction != repoActionLaunch {
		t.Fatalf("repoAction = %v, want launch", a.providerDetailState.repoAction)
	}

	// Simulate typing a repo line and verify the parser would resolve it to
	// the expected repo and port.
	a.editor.SetValue("unsloth/gemma-3-12b --port 9999")
	a.handleProviderDetailKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.providerDetailState.repoMode {
		t.Fatal("repo editor should close after commit")
	}

	_, flags := parseLocalModelArgs("launch unsloth/gemma-3-12b --port 9999")
	if flags.repo != "unsloth/gemma-3-12b" {
		t.Fatalf("repo = %q, want unsloth/gemma-3-12b", flags.repo)
	}
	if flags.port != "9999" {
		t.Fatalf("port = %q, want 9999", flags.port)
	}
}

func TestProvidersMasterListHasAddNewRow(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.push(viewProviders)
	if len(a.providersState.rows) == 0 || !a.providersState.rows[0].addNew {
		t.Fatalf("first row should be the add-new entry: %+v", a.providersState.rows)
	}
}

func TestProviderNewFormCommitsProfileAndLabel(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	a.openProviderNew()

	a.setProviderNewField("host", "localhost")
	a.setProviderNewField("port", "11435")
	a.setProviderNewField("display", "GPU Ollama")
	a.setProviderNewField("name", "ollama-gpu")
	a.providerNewState.displayAuto = false
	a.providerNewState.nameAuto = false

	_, _ = a.providerNewCommit()

	prof, ok := a.settings.Providers["ollama-gpu"]
	if !ok {
		t.Fatalf("committed profile not in settings: %+v", a.settings.Providers)
	}
	if prof.Kind != "ollama" || prof.Host != "localhost" || prof.Port != "11435" {
		t.Fatalf("profile = %+v", prof)
	}
	if prof.BaseURL != "http://localhost:11435/v1" {
		t.Fatalf("BaseURL = %q", prof.BaseURL)
	}
	if a.settings.ProviderLabels["ollama-gpu"] != "GPU Ollama" {
		t.Fatalf("label = %q", a.settings.ProviderLabels["ollama-gpu"])
	}
}

func TestProviderNewAcceptsSecondInstanceOfSameKind(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	for i, slug := range []string{"ollama-gpu", "ollama-cpu"} {
		a.openProviderNew()
		a.setProviderNewField("host", "localhost")
		a.setProviderNewField("port", []string{"11435", "11436"}[i])
		a.setProviderNewField("display", strings.ToUpper(slug))
		a.setProviderNewField("name", slug)
		a.providerNewState.displayAuto = false
		a.providerNewState.nameAuto = false
		_, _ = a.providerNewCommit()
		if _, ok := a.settings.Providers[slug]; !ok {
			t.Fatalf("%s not committed", slug)
		}
	}
	if len(a.settings.Providers) != 2 {
		t.Fatalf("providers = %+v, want both instances", a.settings.Providers)
	}
}

func TestAvailableProvidersTreatsKindCustomAsLocal(t *testing.T) {
	a := newAvailabilityApp(t,
		map[string]bool{"openai": true, "ollama-gpu": true},
		map[string]bool{"ollama-gpu": false},
	)
	a.settings.Providers = map[string]config.ProviderProfile{
		"ollama-gpu": {BaseURL: "http://localhost:11435/v1", API: wire.SurfaceOpenAIChat, Kind: "ollama"},
	}
	if !a.isLocalProvider("ollama-gpu") {
		t.Fatal("kind:ollama custom must be local")
	}

	got := a.availableProviders("")
	for _, name := range got {
		if name == "ollama-gpu" {
			t.Fatal("kind'd local custom with no answering server must be filtered out")
		}
	}

	a.avail.local["ollama-gpu"] = true
	got = a.availableProviders("")
	found := false
	for _, name := range got {
		if name == "ollama-gpu" {
			found = true
		}
	}
	if !found {
		t.Fatal("kind'd local custom with a live server must be offered")
	}
}
