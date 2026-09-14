package tui

import "testing"

func TestNewAppView(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	if a == nil {
		t.Fatalf("NewApp returned nil")
	}
	if v := a.View(); v == "" {
		t.Fatalf("View returned empty string")
	}
}
