package tools

import "testing"

func TestArgStringCanonicalWinsOverAlias(t *testing.T) {
	got, ok := argString(map[string]any{"file_path": "canonical", "path": "alias"}, "file_path")
	if !ok || got != "canonical" {
		t.Fatalf("argString = (%q, %v), want (canonical, true)", got, ok)
	}
}

func TestArgStringResolvesPathAlias(t *testing.T) {
	got, ok := argString(map[string]any{"path": "alias"}, "file_path")
	if !ok || got != "alias" {
		t.Fatalf("argString = (%q, %v), want (alias, true)", got, ok)
	}
}

func TestArgStringResolvesFilePathAlias(t *testing.T) {
	got, ok := argString(map[string]any{"file_path": "canonical"}, "path")
	if !ok || got != "canonical" {
		t.Fatalf("argString = (%q, %v), want (canonical, true)", got, ok)
	}
}

func TestArgStringMissing(t *testing.T) {
	if got, ok := argString(map[string]any{"content": "x"}, "path"); ok || got != "" {
		t.Fatalf("argString = (%q, %v), want missing", got, ok)
	}
}

func TestArgStringNonStringIsAbsent(t *testing.T) {
	if got, ok := argString(map[string]any{"path": 42}, "path"); ok || got != "" {
		t.Fatalf("argString = (%q, %v), want absent", got, ok)
	}
}
