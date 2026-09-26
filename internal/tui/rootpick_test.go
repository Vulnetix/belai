package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/projectregistry"
)

// outsideRootFixture lays out base/{work,sib/notes.md,.hidden/,top.md} and
// returns an App rooted at base/work with guardrails off, so an admitted file
// is read without a classifier round trip.
func outsideRootFixture(t *testing.T) (a *App, base, work, sib string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work = filepath.Join(base, "work")
	sib = filepath.Join(base, "sib")
	for _, d := range []string{work, sib, filepath.Join(base, ".hidden")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sib, "notes.md"), []byte("sibling notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "top.md"), []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	a = New(Options{Workdir: work})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	off := false
	a.guardrailsOverride = &off
	a.syncPosture()
	return a, base, work, sib
}

// drain runs cmd and feeds every message it yields back through Update,
// unpacking batches, until nothing is left.
func drain(t *testing.T, a *App, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatal("drain did not settle")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		_, next := a.Update(msg)
		queue = append(queue, next)
	}
}

// loadPathListing types an @ token and installs its directory listing.
func loadPathListing(t *testing.T, a *App, value string) {
	t.Helper()
	a.editor.SetValue(value)
	cmd := a.pathListIfStale()
	if cmd == nil {
		t.Fatalf("pathListIfStale(%q) = nil, want a listing command", value)
	}
	a.Update(cmd())
}

func TestPathTokenListsAboveRootsWithWarning(t *testing.T) {
	a, _, _, _ := outsideRootFixture(t)
	loadPathListing(t, a, "@../")

	cands := a.fileCandidates()
	for _, want := range []string{"../sib/", "../work/", "../top.md"} {
		if !slices.Contains(cands, want) {
			t.Fatalf("candidates = %v, want %q", cands, want)
		}
	}
	if slices.Contains(cands, "../.hidden/") {
		t.Fatalf("hidden entry listed without a dot filter: %v", cands)
	}

	roots := a.rootSet()
	if a.candidateOutsideRoots(roots, "../work/") {
		t.Fatal("the primary root was marked outside the roots")
	}
	if !a.candidateOutsideRoots(roots, "../sib/") || !a.candidateOutsideRoots(roots, "../top.md") {
		t.Fatal("entries above the root were not marked outside")
	}
	if !strings.Contains(a.renderFilePicker(), "⚠") {
		t.Fatal("picker did not render the outside-root warning")
	}
}

func TestPathTokenDotShowsHidden(t *testing.T) {
	a, _, _, _ := outsideRootFixture(t)
	loadPathListing(t, a, "@../.")
	if cands := a.fileCandidates(); !slices.Equal(cands, []string{"../.hidden/"}) {
		t.Fatalf("candidates = %v, want [../.hidden/]", cands)
	}
}

func TestPathTokenHomeExpands(t *testing.T) {
	a, base, _, _ := outsideRootFixture(t)
	t.Setenv("HOME", base)
	loadPathListing(t, a, "@~/s")
	if cands := a.fileCandidates(); !slices.Equal(cands, []string{"~/sib/"}) {
		t.Fatalf("candidates = %v, want [~/sib/]", cands)
	}
}

func TestPathTokenTabDescends(t *testing.T) {
	a, _, _, _ := outsideRootFixture(t)
	loadPathListing(t, a, "@../s")
	a.fileIndex = 0

	cmd, handled := a.handleFilePickKey(tea.KeyMsg{Type: tea.KeyTab})
	if !handled {
		t.Fatal("tab was not handled")
	}
	if got := a.editor.Value(); got != "@../sib/" {
		t.Fatalf("editor = %q, want @../sib/", got)
	}
	if cmd == nil {
		t.Fatal("descending did not load the directory")
	}
	a.Update(cmd())
	if cands := a.fileCandidates(); !slices.Equal(cands, []string{"../sib/notes.md"}) {
		t.Fatalf("candidates = %v, want [../sib/notes.md]", cands)
	}
	if len(a.attachments) != 0 {
		t.Fatal("descending must not start an attachment")
	}
}

func TestOutsideRootAttachmentWaitsForConfirmation(t *testing.T) {
	a, _, _, sib := outsideRootFixture(t)
	a.editor.SetValue("read @../sib/notes.md ")
	if cmd := a.syncAttachments(); cmd != nil {
		t.Fatal("an outside-root attachment must not be read before confirmation")
	}
	att, ok := a.pendingRootConfirm()
	if !ok {
		t.Fatal("expected a pending root confirmation")
	}
	if att.rootDir != sib {
		t.Fatalf("rootDir = %q, want %q", att.rootDir, sib)
	}
	if !a.rootConfirmVisible() || !strings.Contains(a.renderRootConfirm(), sib) {
		t.Fatal("confirmation pane not shown")
	}
	if !strings.Contains(a.renderAttachStrip(), "⚠") {
		t.Fatal("attachment strip did not mark the waiting attachment")
	}

	a.pendingInput = "read @../sib/notes.md"
	if cmd := a.flushPendingSubmit(); cmd != nil || a.pendingInput == "" {
		t.Fatal("submit went out before the root was confirmed")
	}
}

func TestOutsideRootAttachmentSessionOnly(t *testing.T) {
	a, _, work, sib := outsideRootFixture(t)
	a.editor.SetValue("read @../sib/notes.md ")
	a.syncAttachments()

	drain(t, a, a.handleRootConfirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}))

	if !slices.Contains(a.workspaceDirs, sib) {
		t.Fatalf("workspaceDirs = %v, want %s", a.workspaceDirs, sib)
	}
	if got := projectregistry.WorkspaceDirs(work); len(got) != 0 {
		t.Fatalf("session-only root was persisted: %v", got)
	}
	for _, att := range a.attachments {
		if att.state != attachSafe || att.body != "sibling notes" {
			t.Fatalf("attachment state=%d body=%q reason=%q, want safe", att.state, att.body, att.reason)
		}
		if att.root != sib {
			t.Fatalf("attachment root = %q, want %q", att.root, sib)
		}
	}
	if a.rootConfirmVisible() {
		t.Fatal("confirmation still visible after answering")
	}
}

func TestOutsideRootAttachmentPersisted(t *testing.T) {
	a, _, work, sib := outsideRootFixture(t)
	a.editor.SetValue("read @../sib/notes.md ")
	a.syncAttachments()

	drain(t, a, a.handleRootConfirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}))

	if got := projectregistry.WorkspaceDirs(work); !slices.Contains(got, sib) {
		t.Fatalf("registry workspace dirs = %v, want %s", got, sib)
	}
	for _, att := range a.attachments {
		if att.state != attachSafe {
			t.Fatalf("attachment state=%d reason=%q, want safe", att.state, att.reason)
		}
	}
}

func TestOutsideRootAttachmentDeclined(t *testing.T) {
	a, _, _, _ := outsideRootFixture(t)
	a.editor.SetValue("read @../sib/notes.md ")
	a.syncAttachments()

	a.handleRootConfirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	if len(a.workspaceDirs) != 0 {
		t.Fatalf("declined root was added: %v", a.workspaceDirs)
	}
	for _, att := range a.attachments {
		if att.state != attachRejected || !strings.Contains(att.reason, "declined") {
			t.Fatalf("attachment state=%d reason=%q, want declined rejection", att.state, att.reason)
		}
	}
}

func TestOutsideRootAncestorRefusedWithoutPrompt(t *testing.T) {
	a, _, _, _ := outsideRootFixture(t)
	a.editor.SetValue("read @../top.md ")
	a.syncAttachments()

	if a.rootConfirmVisible() {
		t.Fatal("an ancestor of the workdir must not be offered as a root")
	}
	for _, att := range a.attachments {
		if att.state != attachRejected || !strings.Contains(att.reason, "escapes") {
			t.Fatalf("attachment state=%d reason=%q, want an escape rejection", att.state, att.reason)
		}
	}
}

func TestAddDirRefusesAncestorOfWorkdir(t *testing.T) {
	a, base, work, _ := outsideRootFixture(t)
	msg := a.addWorkspaceDirCmd(base, true)()
	added, ok := msg.(workspaceDirAddedMsg)
	if !ok || added.err == nil {
		t.Fatalf("msg = %#v, want an error", msg)
	}
	if got := projectregistry.WorkspaceDirs(work); len(got) != 0 {
		t.Fatalf("ancestor root was persisted: %v", got)
	}
}
