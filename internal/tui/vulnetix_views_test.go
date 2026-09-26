package tui

import (
	"os"
	"path/filepath"
	"strings"
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

// Opening the artifacts view loads the session project's summary: a review
// pushes it right after writing artifacts, and it used to render "no
// artifact summary loaded" because nothing ever loaded one on that path.
func TestVulnetixArtifactsViewLoadsOnOpen(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	workdir := t.TempDir()
	vdir := filepath.Join(workdir, ".vulnetix")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sarif := `{"runs":[{"tool":{"driver":{"name":"sast","rules":[]}},"results":[{"ruleId":"S1","level":"error"}]}]}`
	if err := os.WriteFile(filepath.Join(vdir, "sast.sarif"), []byte(sarif), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: workdir})
	cmd := a.push(viewVulnetixArtifacts)
	if cmd == nil || !a.vulnetixArtifactsState.loading {
		t.Fatal("opening the view must start loading its summary")
	}
	a.Update(cmd())
	view := a.vulnetixArtifactsView()
	if strings.Contains(view, "no artifact summary loaded") || !strings.Contains(view, "sast.sarif") {
		t.Fatalf("view did not show the project's artifacts:\n%s", view)
	}
}

// The project list opens the view for the chosen project, not the session's.
func TestVulnetixListEnterOpensArtifactsForProject(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	other := t.TempDir()
	a.push(viewVulnetixList)
	a.vulnetixListState = vulnetixListState{rows: []vulnetixListRow{{entry: projectregistry.Entry{Name: "other", Path: other}}}}
	_, cmd := a.handleVulnetixListKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.view != viewVulnetixArtifacts || cmd == nil {
		t.Fatalf("enter must open the artifacts view, view = %v", a.view)
	}
	msg, ok := cmd().(artifactsLoadedMsg)
	if !ok || msg.summary.Dir != other {
		t.Fatalf("loaded %+v, want the chosen project %s", msg.summary.Dir, other)
	}
}
