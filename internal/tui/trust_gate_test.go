package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/projectregistry"
	"github.com/vulnetix/signet/internal/trustgate"
)

func TestTrustGateUntrustedDefaultIsDecline(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	dir := t.TempDir()
	st, err := trustgate.Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	m := trustGateModel{st: st, selected: trustGateDecline}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	fm := out.(trustGateModel)
	if fm.proceed {
		t.Fatal("enter on the default No must not proceed")
	}
	trusted, _, _, err := projectregistry.TrustOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Fatal("declining must not grant trust")
	}
}

func TestTrustGateUntrustedAcceptGrantsTrust(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	dir := t.TempDir()
	extra := t.TempDir()
	if err := config.SaveProject(dir, config.Settings{WorkspaceDirs: []string{extra}}); err != nil {
		t.Fatal(err)
	}
	st, err := trustgate.Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	m := trustGateModel{st: st, selected: trustGateDecline}
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = m2.(trustGateModel)
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	fm := out.(trustGateModel)
	if !fm.proceed {
		t.Fatal("accepting must proceed")
	}
	trusted, accepted, _, err := projectregistry.TrustOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !trusted {
		t.Fatal("accepting must grant trust")
	}
	if len(accepted) != 1 {
		t.Fatalf("accepted = %v, want the proposed dir", accepted)
	}
}

func TestTrustGateTrustedDeclineRecordsAndContinues(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	dir := t.TempDir()
	extra := t.TempDir()
	if err := trustgate.Grant(dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProject(dir, config.Settings{WorkspaceDirs: []string{extra}}); err != nil {
		t.Fatal(err)
	}
	st, err := trustgate.Check(dir)
	if err != nil {
		t.Fatal(err)
	}

	m := trustGateModel{st: st, selected: trustGateDecline}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	fm := out.(trustGateModel)
	if !fm.proceed {
		t.Fatal("declining new proposals on a trusted dir must continue")
	}
	_, _, declined, err := projectregistry.TrustOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(declined) != 1 {
		t.Fatalf("declined = %v, want the proposed dir", declined)
	}
}

func TestTrustGateRendersDirsPanelOnlyWhenProposed(t *testing.T) {
	withDirs := trustGateModel{st: trustgate.Status{Workdir: "/w", Trusted: true, NewDirs: []string{"/w/extra"}}, width: 80}
	view := withDirs.View()
	if !strings.Contains(view, "adds 1 directory") {
		t.Fatal("proposed dirs must render the amber panel")
	}
	without := trustGateModel{st: trustgate.Status{Workdir: "/w", Trusted: true}, width: 80}
	if strings.Contains(without.View(), "adds 1 directory") {
		t.Fatal("no proposed dirs must not render the panel")
	}
}
