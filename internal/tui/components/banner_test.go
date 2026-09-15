package components

import (
	"strings"
	"testing"
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
