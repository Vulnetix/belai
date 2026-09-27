package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/kiroauth"
)

func TestKiroProfilePickerStoresTheChoice(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a.openProviderDetail("kiro")
	a.providerDetailState.backend = credentials.SourceUserFile

	login := kiroauth.Login{RefreshToken: "rt", ClientID: "c", ClientSecret: "s", Region: "us-east-1"}
	profiles := []kiroauth.Profile{
		{ARN: "arn:aws:codewhisperer:us-east-1:1:profile/A", Name: "Alpha"},
		{ARN: "arn:aws:codewhisperer:eu-central-1:1:profile/B", Name: "Beta"},
	}
	a.Update(kiroProfilesMsg{login: login, profiles: profiles})
	if !a.kiroPickingProfile() || !strings.Contains(a.providerDetailView(), "Beta") {
		t.Fatal("picker not shown")
	}
	a.handleProviderDetailKey(tea.KeyMsg{Type: tea.KeyDown})
	a.handleProviderDetailKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.kiroPickingProfile() {
		t.Fatal("picker still open")
	}
	stored, _, ok := resolver.Lookup("kiro", "login")
	if !ok {
		t.Fatalf("login not stored; status %q", a.kiroLogin.status)
	}
	l, err := kiroauth.ParseLogin(stored)
	if err != nil || l.ProfileARN != profiles[1].ARN || l.APIRegion != "eu-central-1" {
		t.Fatalf("stored = %v, %v", l, err)
	}
	if strings.Contains(a.providerDetailView(), "rt") && strings.Contains(a.providerDetailView(), `"refresh_token"`) {
		t.Fatal("the view rendered the login")
	}
}
