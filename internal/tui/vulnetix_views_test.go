package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/projectregistry"
	"github.com/vulnetix/signet/internal/scanartifacts"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

func TestVulnetixConfigViewSmoke(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.vulnetixConfigState = vulnetixConfigState{
		cap: vulnetixcli.Capabilities{Present: true, Version: vulnetixcli.Version{Major: 3, Minor: 107, Patch: 2}},
	}
	_ = a.vulnetixConfigView()
}

func TestVulnetixListViewSmoke(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.vulnetixListState = vulnetixListState{
		rows: []vulnetixListRow{
			{entry: projectregistry.Entry{Name: "signet", Path: "/x/signet", LastSeen: time.Now()}},
		},
	}
	_ = a.vulnetixListView()
}

func TestVulnetixArtifactsViewSmoke(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.vulnetixArtifactsState = vulnetixArtifactsState{
		summary: scanartifacts.Summary{
			Dir:       "/x/proj",
			Artifacts: []scanartifacts.Artifact{{Rel: "sbom.cdx.json"}},
			PerFile:   map[string]scanartifacts.FileSummary{"sbom.cdx.json": {Counts: scanartifacts.Counts{High: 1}}},
		},
	}
	_ = a.vulnetixArtifactsView()
}

func TestVulnetixConfigKeyNavigation(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.push(viewVulnetixConfig)
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("esc should pop to chat, got %v", a.view)
	}
}
