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
	if !strings.Contains(v, "SIGNET") {
		t.Fatalf("text view should contain SIGNET")
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
	if lipgloss.Height(without) != 8 {
		t.Fatalf("expected banner height 8 without version, got %d", lipgloss.Height(without))
	}
	if lipgloss.Height(with) != 9 {
		t.Fatalf("expected banner height 9 with version, got %d", lipgloss.Height(with))
	}
}
