package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/commands"
	"github.com/vulnetix/belai/internal/tui/components"
)

func TestLinkAtColumn(t *testing.T) {
	line := components.SourceLine{Text: "see https://a.example/x, then https://b.example/y.", Col: 2}
	start := 2 + len("see ")
	if got := linkAt(line, start); got != "https://a.example/x" {
		t.Fatalf("at start = %q", got)
	}
	if got := linkAt(line, start+len("https://a.example/x")); got != "" {
		t.Fatalf("on the comma = %q", got)
	}
	if got := linkAt(line, 2+strings.Index(line.Text, "b.example")); got != "https://b.example/y" {
		t.Fatalf("second = %q", got)
	}
	if got := linkAt(components.SourceLine{Text: "http://plain.example/ ftp://x"}, 3); got != "" {
		t.Fatalf("non-https = %q", got)
	}
}

func hoverAnyLine(t *testing.T, a *App, owner int) int {
	t.Helper()
	for i, l := range a.lastFrame.lines {
		if l.Owner == owner && !l.Chrome && l.Width > 0 {
			return i
		}
	}
	t.Fatalf("no line for owner %d", owner)
	return 0
}

func TestHoverOffersThePanelsOnlyLink(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width, a.height = 120, 40
	a.messages = []components.Message{
		{Role: "assistant", Content: "Scan finished.\n\nsnapshot · https://www.vulnetix.com/snapshots/abc"},
		{Role: "assistant", Content: "two links: https://a.example/1 and https://b.example/2"},
	}
	renderFrame(t, a)
	pointAt(a, hoverAnyLine(t, a, 0))
	a.recomputeHover()
	if a.hover.link != "https://www.vulnetix.com/snapshots/abc" {
		t.Fatalf("hover link = %q", a.hover.link)
	}
	if hint := a.hoverHint(); !strings.Contains(hint, "ctrl+y") || !strings.Contains(hint, "open link") {
		t.Fatalf("hint = %q", hint)
	}
	// Several links and the pointer on none of them: nothing is guessed.
	l := hoverAnyLine(t, a, 1)
	a.mouseX = a.lastFrame.left + a.lastFrame.lines[l].Col
	a.mouseY = a.lastFrame.top + l
	a.recomputeHover()
	if a.hover.link != "" {
		t.Fatalf("ambiguous panel offered %q", a.hover.link)
	}
}

func TestReviewCardShowsSnapshots(t *testing.T) {
	card := reviewScanCard(commands.ScanOutcome{Name: "sast", Snapshots: []commands.Snapshot{
		{Label: "SAST", URL: "https://www.vulnetix.com/snapshots/s"},
		{URL: "https://www.vulnetix.com/snapshots/t"},
	}}, reviewCard{headline: "3 findings"}, nil, "")
	for _, want := range []string{"SAST snapshot · https://www.vulnetix.com/snapshots/s", "- snapshot · https://www.vulnetix.com/snapshots/t"} {
		if !strings.Contains(card, want) {
			t.Fatalf("card lacks %q:\n%s", want, card)
		}
	}
}

func TestRunsOutputSnapshotPrefersTheVisibleOne(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width, a.height = 120, 40
	a.runsOutput.content = "SCA snapshot: https://www.vulnetix.com/s/1\n" + strings.Repeat("row\n", 80) +
		"SAST snapshot: https://www.vulnetix.com/s/2\n"
	a.enterRunsOutput()
	if got := a.runsOutputSnapshot(); got != "https://www.vulnetix.com/s/1" {
		t.Fatalf("top = %q", got)
	}
	a.runsOutput.vp.GotoBottom()
	if got := a.runsOutputSnapshot(); got != "https://www.vulnetix.com/s/2" {
		t.Fatalf("bottom = %q", got)
	}
	a.runsOutput.vp.SetYOffset(30)
	if got := a.runsOutputSnapshot(); got != "https://www.vulnetix.com/s/2" {
		t.Fatalf("none on screen = %q, want the last", got)
	}
	if !strings.Contains(a.runsOutputView(), "open snapshot") {
		t.Fatal("help bar lacks the snapshot key")
	}
}

func TestRCFooterAndCommand(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.width, a.height = 120, 40
	a.pollRC(true)
	if a.rcFooterLabel() != "" {
		t.Fatalf("footer label with no daemon = %q", a.rcFooterLabel())
	}
	a.rcLive, a.rcSessions = true, 2
	if a.rcFooterLabel() != "rc 2" {
		t.Fatalf("footer label = %q", a.rcFooterLabel())
	}
	a.rcLive = false
	a.rcCommand("status")
	last := a.messages[len(a.messages)-1].Content
	if !strings.Contains(last, "not running") || !strings.Contains(last, "/resolve/belai-sessions") {
		t.Fatalf("status = %q", last)
	}
	a.rcCommand("")
	if a.view != viewRC || a.rcState.page != rcIntro {
		t.Fatalf("view = %v page = %v", a.view, a.rcState.page)
	}
	out := a.rcView()
	for _, want := range []string{"Remote control", "belai-sessions", "vulnetix auth login", "asks"} {
		if !strings.Contains(out, want) {
			t.Fatalf("intro lacks %q", want)
		}
	}
}
