package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/credentials"
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
