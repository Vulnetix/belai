package scanartifacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMemorySummary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.yaml")
	data := []byte(`last_scan:
  timestamp: "2026-09-18T12:00:00Z"
  packages: 120
  vulns: 15
  critical: 1
  high: 2
  medium: 7
  low: 5
history:
  - ...
findings:
  - ...
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ParseMemorySummary(path)
	if err != nil {
		t.Fatalf("ParseMemorySummary: %v", err)
	}
	if m.Packages != 120 || m.Vulns != 15 || m.Critical != 1 || m.High != 2 || m.Medium != 7 || m.Low != 5 {
		t.Fatalf("unexpected summary: %+v", m)
	}
}

func TestParseMemorySummaryMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.yaml")
	if err := os.WriteFile(path, []byte("history:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMemorySummary(path); err == nil {
		t.Fatal("expected error for missing last_scan")
	}
}
