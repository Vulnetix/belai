package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/trustgate"
)

// knowledgeScreenApp is an App on its own BELAI_HOME whose project has one
// indexed document, plus a second trusted project and an agent profile that
// each have an index of their own. The screen is not opened yet.
func knowledgeScreenApp(t *testing.T) (*App, string, string) {
	t.Helper()
	a, dir := knowledgeTUI(t)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if err := os.MkdirAll(filepath.Join(dir, ".vulnetix"), 0o700); err != nil {
		t.Fatal(err)
	}
	mem := filepath.Join(dir, ".vulnetix", "memory.yaml")
	if err := os.WriteFile(mem, []byte("scan memory for the repository\nlast review found an authentication weakness in the login handler\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.knowledgeStore().Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A second project with its own index on this host.
	other := t.TempDir()
	if err := trustgate.Grant(other, nil); err != nil {
		t.Fatal(err)
	}
	ix := knowledge.NewIndex(knowledge.ProjectScope)
	in := knowledge.Input{Address: "kb+project/.vulnetix/sbom.cdx.json", Source: filepath.Join(other, ".vulnetix", "sbom.cdx.json"), Size: 20,
		SHA: "x", Chunks: knowledge.SplitText("cyclonedx sbom component purl for the dependency inventory")}
	in.Artifact, in.Tool = "cyclonedx-sbom", "syft"
	if _, err := ix.Ingest(context.Background(), in, nil, 0); err != nil {
		t.Fatal(err)
	}
	pdir, err := config.ProjectKnowledgeDir(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Save(pdir); err != nil {
		t.Fatal(err)
	}

	// An agent with documents.
	docs := t.TempDir()
	if err := os.WriteFile(filepath.Join(docs, "auth.md"), []byte("# Authentication\n\nUsers log in with a password and a totp code.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := agentprofile.AgentProfile{
		Name: "reviewer", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle,
		Knowledge: &agentprofile.KnowledgeSpec{Paths: []string{docs}},
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := agentprofile.Load("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	store := knowledge.Open(knowledge.Options{Profile: &knowledge.Profile{ID: saved.ID, Name: saved.Name, Paths: saved.KnowledgePaths()}})
	if _, err := store.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, other, saved.ID
}

// openKnowledgeScreen opens the screen and applies the load it starts.
func openKnowledgeScreen(t *testing.T, a *App, arg string) {
	t.Helper()
	cmd := a.handleCommand("/knowledge " + arg)
	if a.view != viewKnowledge {
		t.Fatalf("view = %v, want knowledge", a.view)
	}
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(knowledgeLoadedMsg); ok {
			a.handleKnowledgeMsg(msg)
			return
		}
		cmd = nil
	}
}

func knowKey(t *testing.T, a *App, keys ...string) {
	t.Helper()
	for _, k := range keys {
		switch k {
		case "tab":
			a.handleKnowledgeKey(tea.KeyMsg{Type: tea.KeyTab})
		case "enter", "esc":
			a.handleKnowledgeKey(key(k))
		default:
			a.handleKnowledgeKey(key(k))
		}
	}
}

func typeKnowledge(t *testing.T, a *App, s string) {
	t.Helper()
	for _, r := range s {
		if r == ' ' {
			a.handleKnowledgeKey(tea.KeyMsg{Type: tea.KeySpace})
			continue
		}
		a.handleKnowledgeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestKnowledgeCommandOpensTheScreenOnAScope(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	for arg, want := range map[string]int{"": knowTabProject, "project": knowTabProject, "global": knowTabGlobal, "agents": knowTabAgents} {
		a.popToChat()
		openKnowledgeScreen(t, a, arg)
		if a.knowledgeState.tab != want {
			t.Errorf("/knowledge %s opened tab %d, want %d", arg, a.knowledgeState.tab, want)
		}
	}
	var listed bool
	for _, n := range NewRegistry(t.TempDir()).Names() {
		listed = listed || n == "knowledge"
	}
	if !listed {
		t.Fatal("/knowledge is not in the command list")
	}
	var have bool
	for _, e := range screenEntries {
		if e.key == "n" && e.view == viewKnowledge {
			have = true
		}
	}
	if !have {
		t.Fatal("the screen switcher has no knowledge entry")
	}
}

func TestKnowledgeProjectTabListsDocumentsWithTheirTags(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	openKnowledgeScreen(t, a, "project")
	shown := a.knowledgeShown()
	var found bool
	for _, r := range shown {
		if r.kind == knowRowDoc && strings.HasSuffix(r.doc.Address, ".vulnetix/memory.yaml") {
			found = true
			if r.doc.Tags.Kind != "scanner" || !r.doc.Tags.Has("kind:memory") {
				t.Fatalf("tags = %+v", r.doc.Tags)
			}
		}
	}
	if !found {
		t.Fatalf("the project's document is not listed: %d rows", len(shown))
	}
	view := a.knowledgeView()
	for _, want := range []string{"Knowledge", "project", "global", "agents", "memory.yaml", "scanner"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	detail := strings.Join(a.knowledgeDetail(shown), "\n")
	if !strings.Contains(detail, "kind:memory") || !strings.Contains(detail, "type scanner") {
		t.Fatalf("detail:\n%s", detail)
	}
}

func TestKnowledgeFilterMatchesWordsAndLabels(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	openKnowledgeScreen(t, a, "project")
	total := len(a.knowledgeShown())
	knowKey(t, a, "/")
	typeKnowledge(t, a, "type:scanner")
	if n := len(a.knowledgeShown()); n == 0 || n > total {
		t.Fatalf("type:scanner shows %d of %d", n, total)
	}
	knowKey(t, a, "esc")
	if a.knowledgeState.filter != "" || a.knowledgeState.filtering {
		t.Fatal("esc must clear the filter")
	}
	knowKey(t, a, "/")
	typeKnowledge(t, a, "type:test")
	if n := len(a.knowledgeShown()); n != 0 {
		t.Fatalf("type:test shows %d, want 0", n)
	}
	if !strings.Contains(a.knowledgeView(), "Nothing matches") {
		t.Fatal("an empty filter result must say so")
	}
}

func TestKnowledgeGlobalListsEveryProjectAndPicksOne(t *testing.T) {
	a, other, _ := knowledgeScreenApp(t)
	openKnowledgeScreen(t, a, "global")
	rows := a.knowledgeShown()
	if len(rows) < 2 {
		t.Fatalf("global rows = %d, want this project and the other", len(rows))
	}
	for _, r := range rows {
		if r.kind != knowRowIndex {
			t.Fatal("the unfiltered global list is of projects, not documents")
		}
	}
	if !a.knowledgeEntries()[rows[0].entry].current {
		t.Fatal("this project is listed first")
	}
	// Filter to the other project by its whole path and open it. The last
	// element alone ("002") is also a substring of the random directory both
	// temp dirs share, so it matched this project too about one run in a hundred.
	knowKey(t, a, "/")
	typeKnowledge(t, a, other)
	knowKey(t, a, "enter")
	if n := len(a.knowledgeShown()); n != 1 {
		t.Fatalf("filtered to %d projects, want 1", n)
	}
	knowKey(t, a, "enter")
	if a.knowledgePick() == "" {
		t.Fatal("enter must pick the project")
	}
	docs := a.knowledgeShown()
	if len(docs) != 1 || docs[0].kind != knowRowDoc || !strings.HasSuffix(docs[0].doc.Address, "sbom.cdx.json") {
		t.Fatalf("documents of the picked project: %+v", docs)
	}
	if !docs[0].doc.Tags.Has("tool:syft") {
		t.Fatalf("tags %v", docs[0].doc.Tags.Labels)
	}
	knowKey(t, a, "esc")
	if a.knowledgePick() != "" || a.view != viewKnowledge {
		t.Fatal("esc widens to every project first")
	}
	knowKey(t, a, "esc")
	if a.view == viewKnowledge {
		t.Fatal("esc on the full list leaves the screen")
	}
}

func TestKnowledgeAgentsTabListsProfilesWithDocuments(t *testing.T) {
	a, _, id := knowledgeScreenApp(t)
	openKnowledgeScreen(t, a, "agents")
	var found bool
	for _, e := range a.knowledgeEntries() {
		if e.info.Key == id {
			found = true
			if e.info.Docs() == 0 {
				t.Fatalf("the agent's index has no documents: %+v", e.info)
			}
		}
	}
	if !found {
		t.Fatalf("the agent is not listed: %d entries", len(a.knowledgeEntries()))
	}
	knowKey(t, a, "/")
	typeKnowledge(t, a, "reviewer")
	knowKey(t, a, "enter", "enter")
	docs := a.knowledgeShown()
	if len(docs) == 0 || docs[0].kind != knowRowDoc || !strings.Contains(docs[0].doc.Address, "reviewer/") {
		t.Fatalf("the agent's documents: %+v", docs)
	}
	if !docs[0].doc.Tags.Has("topic:authn") {
		t.Fatalf("the authentication document was not labelled: %v", docs[0].doc.Tags.All())
	}
}

func TestKnowledgeSearchRunsTheModelToolsOverTheScope(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	openKnowledgeScreen(t, a, "project")

	knowKey(t, a, "s")
	typeKnowledge(t, a, "authentication weakness login")
	knowKey(t, a, "enter")
	st := &a.knowledgeState
	if !st.ran || len(st.result) == 0 || !strings.Contains(st.resultCmd, "Grep") {
		t.Fatalf("grep: ran=%v cmd=%q", st.ran, st.resultCmd)
	}
	out := strings.Join(st.result, "\n")
	if !strings.Contains(out, tools.KnowledgeRowsHeader) || !strings.Contains(out, "kb+project/.vulnetix/memory.yaml:") {
		t.Fatalf("grep output is not what a model gets:\n%s", out)
	}

	knowKey(t, a, "m")
	if st.mode != knowModeGlob || !strings.Contains(st.resultCmd, "Glob") {
		t.Fatalf("mode %d cmd %q", st.mode, st.resultCmd)
	}
	if out := strings.Join(st.result, "\n"); !strings.Contains(out, tools.KnowledgeFilesHeader) || !strings.Contains(out, "kb+project/.vulnetix/memory.yaml") {
		t.Fatalf("glob output:\n%s", out)
	}

	knowKey(t, a, "s")
	for range st.query {
		a.handleKnowledgeKey(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeKnowledge(t, a, "memory.yaml")
	a.handleKnowledgeKey(tea.KeyMsg{Type: tea.KeyTab}) // read
	a.handleKnowledgeKey(key("enter"))
	if st.mode != knowModeRead || !strings.Contains(strings.Join(st.result, "\n"), "lines of indexed text") {
		t.Fatalf("read: mode %d result %v", st.mode, st.result)
	}
}

func TestKnowledgeSearchFindsDocumentsByLabel(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	openKnowledgeScreen(t, a, "project")
	knowKey(t, a, "s")
	typeKnowledge(t, a, "kind memory type scanner")
	knowKey(t, a, "enter")
	out := strings.Join(a.knowledgeState.result, "\n")
	if !strings.Contains(out, "kb+project/.vulnetix/memory.yaml:0:labels:") {
		t.Fatalf("a search by label must reach the label row:\n%s", out)
	}
}

func TestKnowledgeResultOpensItsDocumentAndEscClosesLayersInOrder(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	openKnowledgeScreen(t, a, "project")
	knowKey(t, a, "s")
	typeKnowledge(t, a, "authentication weakness login")
	knowKey(t, a, "enter")
	st := &a.knowledgeState
	for i, l := range st.result {
		if knowledgeAddrRe.MatchString(l) {
			st.resultSel = i
			break
		}
	}
	knowKey(t, a, "enter")
	if !st.showDoc || !strings.Contains(strings.Join(st.docLines, "\n"), "authentication weakness") {
		t.Fatalf("document not opened: show=%v lines=%v", st.showDoc, st.docLines)
	}
	if !strings.Contains(a.knowledgeView(), "indexed text") {
		t.Fatal("the pane does not show the document")
	}
	knowKey(t, a, "esc")
	if st.showDoc || !st.ran {
		t.Fatal("esc closes the document first and keeps the results")
	}
	knowKey(t, a, "esc")
	if st.ran || a.view != viewKnowledge {
		t.Fatal("the next esc clears the results and stays on the screen")
	}
	knowKey(t, a, "esc")
	if a.view == viewKnowledge {
		t.Fatal("the last esc leaves the screen")
	}
}

func TestKnowledgeProjectScopeAppliesReadDenyRules(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	a.settings.Permissions.Deny = []string{"Read(**/memory.yaml)"}
	openKnowledgeScreen(t, a, "project")
	knowKey(t, a, "s")
	typeKnowledge(t, a, "authentication weakness login")
	knowKey(t, a, "enter")
	if out := strings.Join(a.knowledgeState.result, "\n"); strings.Contains(out, "memory.yaml") {
		t.Fatalf("a Read deny rule must hide the document from the project's search:\n%s", out)
	}
}

func TestKnowledgeScreenOnlyReadsIndexes(t *testing.T) {
	a, other, _ := knowledgeScreenApp(t)
	dir, err := config.ProjectKnowledgeDir(other)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(knowledge.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	openKnowledgeScreen(t, a, "global")
	knowKey(t, a, "enter", "s")
	typeKnowledge(t, a, "sbom component")
	knowKey(t, a, "enter")
	after, _ := os.ReadFile(knowledge.Path(dir))
	if string(before) != string(after) {
		t.Fatal("browsing changed an index file")
	}
}

func TestKnowledgeCorruptIndexIsListedAndLeftAlone(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	bad := t.TempDir()
	if err := trustgate.Grant(bad, nil); err != nil {
		t.Fatal(err)
	}
	dir, err := config.ProjectKnowledgeDir(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("not an index at all")
	if err := os.WriteFile(knowledge.Path(dir), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	openKnowledgeScreen(t, a, "global")
	if v := a.knowledgeView(); !strings.Contains(v, "unreadable") {
		t.Fatalf("a corrupt index must be listed as unreadable:\n%s", v)
	}
	if got, _ := os.ReadFile(knowledge.Path(dir)); string(got) != string(garbage) {
		t.Fatal("a corrupt index must be left as it is")
	}
}

func TestKnowledgeScreenFitsASmallTerminal(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	a.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	openKnowledgeScreen(t, a, "global")
	v := a.knowledgeView()
	if !strings.Contains(v, "Knowledge") {
		t.Fatalf("view:\n%s", v)
	}
	for _, l := range strings.Split(v, "\n") {
		if w := lipgloss.Width(l); w > a.contentWidth()+2 {
			t.Fatalf("line wider than the content area (%d > %d): %q", w, a.contentWidth()+2, l)
		}
	}
	if got := strings.Count(v, "\n") + 1; got > 24 {
		t.Fatalf("the screen is %d lines tall in a 24 line terminal", got)
	}
}

func TestKnowledgeStaleLoadIsDropped(t *testing.T) {
	a, _, _ := knowledgeScreenApp(t)
	openKnowledgeScreen(t, a, "global")
	n := len(a.knowledgeState.projects)
	a.handleKnowledgeMsg(knowledgeLoadedMsg{gen: a.knowledgeState.gen - 1})
	if len(a.knowledgeState.projects) != n {
		t.Fatal("a load from an earlier open of the screen must not replace the current one")
	}
}
