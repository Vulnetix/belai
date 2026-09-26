package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
)

// lastSystemMessage returns the text of the newest system line, or "".
func lastSystemMessage(a *App) string {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "system" {
			return a.messages[i].Text()
		}
	}
	return ""
}

// A working-directory move updates the footer and leaves one informational
// line in the thread, so a reader scrolling back can tell which directory the
// relative paths around it resolved against.
func TestCwdEventUpdatesFooterAndThread(t *testing.T) {
	root := t.TempDir()
	a := New(Options{Workdir: root})
	sub := filepath.Join(root, "internal", "tools")

	a.handleAgentEvent(agentEventMsg{Kind: agent.EventCwdKind, Cwd: "internal/tools", CwdDir: sub})

	if a.cwd != sub {
		t.Errorf("app cwd = %q, want %q", a.cwd, sub)
	}
	if a.footer.Cwd != sub {
		t.Errorf("footer cwd = %q, want %q", a.footer.Cwd, sub)
	}
	if got := lastSystemMessage(a); got != "working directory: /internal/tools" {
		t.Errorf("thread line = %q", got)
	}
}

// Returning to the root says so in words rather than showing a bare slash the
// reader has to interpret.
func TestCwdEventBackToRoot(t *testing.T) {
	root := t.TempDir()
	a := New(Options{Workdir: root})
	a.handleAgentEvent(agentEventMsg{
		Kind: agent.EventCwdKind, Cwd: "internal", CwdDir: filepath.Join(root, "internal"),
	})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventCwdKind, Cwd: "", CwdDir: root})

	if a.cwd != root {
		t.Errorf("app cwd = %q, want %q", a.cwd, root)
	}
	if got := lastSystemMessage(a); got != "working directory: / (session root)" {
		t.Errorf("thread line = %q", got)
	}
}

// A move to where we already are is not announced: repeating the line would
// make the thread noisier without saying anything new.
func TestCwdEventIgnoresNoOpMove(t *testing.T) {
	root := t.TempDir()
	a := New(Options{Workdir: root})
	before := len(a.messages)

	a.handleAgentEvent(agentEventMsg{Kind: agent.EventCwdKind, Cwd: "", CwdDir: root})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventCwdKind, Cwd: "", CwdDir: ""})

	if len(a.messages) != before {
		t.Fatalf("a no-op move added %d messages", len(a.messages)-before)
	}
}

// Rebuilding the agent session builds a fresh tracker rooted at the workdir,
// so the displayed directory has to return there with it.
func TestInvalidateAgentSessionResetsCwd(t *testing.T) {
	root := t.TempDir()
	a := New(Options{Workdir: root})
	a.handleAgentEvent(agentEventMsg{
		Kind: agent.EventCwdKind, Cwd: "internal", CwdDir: filepath.Join(root, "internal"),
	})

	a.invalidateAgentSession()

	if a.cwd != root {
		t.Fatalf("cwd = %q after invalidation, want %q", a.cwd, root)
	}
	if a.footer.Cwd != "" && a.footer.Cwd != root {
		t.Fatalf("footer cwd = %q after invalidation, want %q", a.footer.Cwd, root)
	}
}

// The footer's first line carries the directory, so the memoised footer
// height must be invalidated when it changes or the viewport is sized against
// a footer that is no longer rendered.
func TestCwdEventInvalidatesFooterHeightCache(t *testing.T) {
	root := t.TempDir()
	a := New(Options{Workdir: root})
	a.width = 100
	_ = a.footerHeight() // memoise

	a.handleAgentEvent(agentEventMsg{
		Kind: agent.EventCwdKind, Cwd: "internal", CwdDir: filepath.Join(root, "internal"),
	})

	if a.footerW == a.width {
		t.Fatal("footer height cache survived a working-directory change")
	}
}

// The rendered footer actually shows the new directory, home-shortened the
// same way the starting directory is.
func TestFooterRendersMovedDirectory(t *testing.T) {
	root := t.TempDir()
	a := New(Options{Workdir: root})
	a.width = 120
	sub := filepath.Join(root, "internal")

	a.handleAgentEvent(agentEventMsg{Kind: agent.EventCwdKind, Cwd: "internal", CwdDir: sub})
	a.refreshFooter()

	if !strings.Contains(a.footer.View(), filepath.Base(sub)) {
		t.Fatalf("footer does not show the moved directory:\n%s", a.footer.View())
	}
}

// A session built under an engaged agent's tool allowlist narrows with
// Registry.Only, which keeps the shared working-directory tracker. The
// hand-rolled filter loop that preceded it rebuilt the registry without the
// tracker, so a Cd in an allowlisted session never reached the footer.
func TestAllowlistedSessionKeepsCwdTracker(t *testing.T) {
	root := t.TempDir()
	a := New(Options{Workdir: root})
	params := a.sessionBuildParams()
	params.toolAllow = []string{"Read", "Cd"}

	sess, err := buildAgentSession(params)
	if err != nil {
		t.Fatalf("buildAgentSession: %v", err)
	}
	if sess.Cwd() == nil {
		t.Fatal("an allowlisted session must keep the working-directory tracker")
	}
}

// A session built with workspace dirs must install them as real confinement
// roots, so a trust-activated or restored directory is reachable, not just
// advertised to the model.
func TestBuildAgentSessionAddsWorkspaceRoots(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	root := t.TempDir()
	extra := t.TempDir()
	a := New(Options{Workdir: root})
	a.workspaceDirs = []string{extra}

	sess, err := buildAgentSession(a.sessionBuildParams())
	if err != nil {
		t.Fatalf("buildAgentSession: %v", err)
	}
	roots := sess.Cwd().Roots()
	found := false
	for _, r := range roots {
		if r == extra {
			found = true
		}
	}
	if !found {
		t.Fatalf("roots = %v, want to include %s", roots, extra)
	}
}
