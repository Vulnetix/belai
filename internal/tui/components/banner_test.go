package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestBannerPixViewNotEmpty(t *testing.T) {
	b := Banner{Width: 80}
	v := b.View()
	if v == "" {
		t.Fatal("banner should not be empty")
	}
}

func TestPixGridDimensions(t *testing.T) {
	if len(PixGrid) != 12 {
		t.Fatalf("expected 12 rows, got %d", len(PixGrid))
	}
	for i, row := range PixGrid {
		// counting the non-space characters in the row
		count := 0
		for _, r := range row {
			if r != ' ' {
				count++
			}
		}
		if count != 12 {
			t.Fatalf("row %d should contain 12 pixels, got %d", i, count)
		}
	}
}

func TestTextView(t *testing.T) {
	b := Banner{}
	v := b.textView()
	if !strings.Contains(v, "BELAI") {
		t.Fatalf("text view should contain BELAI")
	}
}

func TestBannerVersionLine(t *testing.T) {
	b := Banner{Width: 80, Version: "0.4.2", Commit: "ab12cd3", Built: "2026-09-15T10:00:00Z"}
	line := b.versionLine()
	if !strings.Contains(line, "v0.4.2") {
		t.Fatalf("expected v0.4.2 in %q", line)
	}
	if !strings.Contains(line, "ab12cd3") || !strings.Contains(line, "2026-09-15T10:00:00Z") {
		t.Fatalf("expected commit and build date in %q", line)
	}
}

func TestBannerVersionLineDropsSentinels(t *testing.T) {
	b := Banner{Width: 80, Version: "dev", Commit: "unknown", Built: "unknown"}
	if b.versionLine() != "" {
		t.Fatalf("expected empty version line for sentinels, got %q", b.versionLine())
	}
}

func TestBannerVersionLineWithVPrefix(t *testing.T) {
	b := Banner{Width: 80, Version: "v0.9.0-dirty", Commit: "ab12cd3", Built: "2026-09-15T10:00:00Z"}
	line := b.versionLine()
	if strings.Contains(line, "vv") {
		t.Fatalf("expected no double v prefix in %q", line)
	}
	if !strings.Contains(line, "v0.9.0-dirty") {
		t.Fatalf("expected v0.9.0-dirty in %q", line)
	}
}

func TestBannerVersionLineTruncatesToWidth(t *testing.T) {
	b := Banner{Width: 20, Version: "0.4.2", Commit: "ab12cd3", Built: "2026-09-15T10:00:00Z"}
	line := b.versionLine()
	w := lipgloss.Width(line)
	if w > 20 {
		t.Fatalf("version line width %d > 20", w)
	}
}

func TestTextViewContainsVersion(t *testing.T) {
	b := Banner{Width: 80, Version: "0.4.2"}
	v := b.textView()
	if !strings.Contains(v, "v0.4.2") {
		t.Fatalf("text view should contain version: %q", v)
	}
}

func TestBannerHeightIsStable(t *testing.T) {
	without := Banner{Width: 80}.pixView()
	with := Banner{Width: 80, Version: "0.4.2"}.pixView()
	// The wordmark block sits beside the owl, so the banner is six rows tall
	// whether or not the build carries a version stamp.
	if lipgloss.Height(without) != 6 {
		t.Fatalf("expected banner height 6 without version, got %d", lipgloss.Height(without))
	}
	if lipgloss.Height(with) != 6 {
		t.Fatalf("expected banner height 6 with version, got %d", lipgloss.Height(with))
	}
}

func TestBannerBelayLine(t *testing.T) {
	v := Banner{Width: 80}.pixView()
	if !strings.Contains(v, "belai") || !strings.Contains(v, "◉") {
		t.Fatalf("banner should draw the belay-line wordmark: %q", v)
	}
	if !strings.Contains(v, "on belay") {
		t.Fatalf("banner should carry the on-belay tagline: %q", v)
	}
}

// The rope shortens on a narrow terminal so the wordmark row never wraps.
func TestBannerRopeFitsWidth(t *testing.T) {
	for _, w := range []int{30, 40, 50, 80, 200} {
		b := Banner{Width: w}
		if got := 12 + 1 + lipgloss.Width(belayLine(b.ropeTail())); got > w {
			t.Fatalf("width %d: wordmark row is %d cells", w, got)
		}
	}
	if full := lipgloss.Width(Banner{}.ropeTail()); full != belayRopeMax+1 {
		t.Fatalf("unbounded rope = %d cells, want %d", full, belayRopeMax+1)
	}
}

func TestBannerVersionLineCarriesUpdateNote(t *testing.T) {
	b := Banner{Width: 80, Version: "0.4.2", Commit: "ab12cd3", Update: "update v0.5.0 available"}
	line := b.versionLine()
	if !strings.Contains(line, "v0.4.2") || !strings.Contains(line, "update v0.5.0 available") {
		t.Fatalf("expected version and update note in %q", line)
	}
}

func TestBannerUpdateNoteWithoutStampedBuild(t *testing.T) {
	b := Banner{Width: 80, Version: "dev", Commit: "unknown", Built: "unknown", Update: "update v0.5.0 available"}
	if !strings.Contains(b.versionLine(), "update v0.5.0 available") {
		t.Fatalf("update note dropped on an unstamped build: %q", b.versionLine())
	}
}

// The banner is six rows tall and the transcript is laid out under it, so an
// update note must share the version row rather than add one.
func TestBannerHeightUnchangedByUpdateNote(t *testing.T) {
	plain := Banner{Width: 80, Version: "0.4.2"}.View()
	noted := Banner{Width: 80, Version: "0.4.2", Update: "update v0.5.0 available"}.View()
	if got, want := lipgloss.Height(noted), lipgloss.Height(plain); got != want {
		t.Fatalf("height with update note = %d, want %d", got, want)
	}
}
