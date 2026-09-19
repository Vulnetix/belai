package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/projectregistry"
	"github.com/vulnetix/signet/internal/scanartifacts"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

func TestCodeReviewConfigViewSmoke(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.codeReviewConfigState = codeReviewConfigState{
		cap: vulnetixcli.Capabilities{Present: true, Version: vulnetixcli.Version{Major: 3, Minor: 107, Patch: 2}},
	}
	_ = a.codeReviewConfigView()
}

func TestCodeReviewListViewSmoke(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.codeReviewListState = codeReviewListState{
		rows: []codeReviewListRow{
			{entry: projectregistry.Entry{Name: "signet", Path: "/x/signet", LastSeen: time.Now()}},
		},
	}
	_ = a.codeReviewListView()
}

func TestCodeReviewArtifactsViewSmoke(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.codeReviewArtifactsState = codeReviewArtifactsState{
		summary: scanartifacts.Summary{
			Dir:       "/x/proj",
			Artifacts: []scanartifacts.Artifact{{Rel: "sbom.cdx.json"}},
			PerFile:   map[string]scanartifacts.FileSummary{"sbom.cdx.json": {Counts: scanartifacts.Counts{High: 1}}},
		},
	}
	_ = a.codeReviewArtifactsView()
}

func TestCodeReviewConfigKeyNavigation(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.push(viewCodeReviewConfig)
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("esc should pop to chat, got %v", a.view)
	}
}
