package scanartifacts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	dir := t.TempDir()

	write := func(name, content string) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// SARIF with two headline results and one suppressed.
	write("sast.sarif", `{
  "runs": [{
    "tool": {"driver": {"rules": []}},
    "results": [
      {"ruleId": "A", "properties": {"severity": "high"}},
      {"ruleId": "B", "level": "error"},
      {"ruleId": "C", "level": "error", "suppressions": [{}]}
    ]
  }]
}`)
	// CycloneDX with one high headline vuln and one license.
	write("sbom.cdx.json", `{"vulnerabilities": [
	  {"id": "CVE-1", "source": {"name": "NVD"}, "ratings": [{"score": 9.0, "method": "CVSSv31", "source": {"name": "NVD"}}]},
	  {"id": "LIC-1", "source": {"name": "vulnetix-license-analyzer"}, "properties": [{"name": "vulnetix:license-severity", "value": "medium"}]}
	]}`)
	// Memory with critical+high.
	write("memory.yaml", `last_scan:
  packages: 10
  vulns: 2
  critical: 1
  high: 1
  medium: 0
  low: 0
`)
	// Superseded SARIF should be excluded.
	write("old.sarif", `{"runs": [{"tool": {"driver": {"rules": []}}, "results": [{"ruleId": "OLD", "level": "error"}]}]}`)
	// Signet-owned file should be excluded.
	write("settings.json", "{}")

	arts, err := Enumerate(dir)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	s := Summarize(context.Background(), dir, arts)

	// Memory: critical=1, high=1.
	// SARIF: high=1 (A), high=1 (B) → but cross-file union dedupes by key,
	// where key differs (ruleId|uri|line). So union should include both.
	// CycloneDX: critical=1.
	if s.Union.Critical != 2 { // CVE-1 + memory critical
		t.Fatalf("union critical = %d, want 2 (%+v)", s.Union.Critical, s.Union)
	}
	if s.Union.High < 2 {
		t.Fatalf("union high = %d, want >= 2 (%+v)", s.Union.High, s.Union)
	}
	if s.Licenses.Medium != 1 {
		t.Fatalf("licenses medium = %d, want 1 (%+v)", s.Licenses.Medium, s.Licenses)
	}
	if s.Suppressed.Total() != 1 {
		t.Fatalf("suppressed total = %d, want 1 (%+v)", s.Suppressed.Total(), s.Suppressed)
	}
	if _, ok := s.PerFile["sast.sarif"]; !ok {
		t.Fatalf("per-file missing sast.sarif: %+v", s.PerFile)
	}
	if _, ok := s.PerFile["settings.json"]; ok {
		t.Fatal("signet-owned settings.json should not appear in per-file")
	}
}

func TestSummarizeSkipReasons(t *testing.T) {
	dir := t.TempDir()
	arts := []Artifact{
		{Path: filepath.Join(dir, "missing.sarif"), Rel: "missing.sarif", Kind: KindSARIF},
		{Path: filepath.Join(dir, "tool.log"), Rel: "tool.log", Kind: KindToolLog},
		{Path: filepath.Join(dir, "unknown.bin"), Rel: "unknown.bin", Kind: KindUnknown},
	}
	s := Summarize(context.Background(), dir, arts)
	if fs := s.PerFile["missing.sarif"]; fs.SkipReason == "" {
		t.Fatal("missing file should have a skip reason")
	}
	if fs := s.PerFile["tool.log"]; fs.SkipReason == "" {
		t.Fatal("tool log should have a skip reason")
	}
	if fs := s.PerFile["unknown.bin"]; fs.SkipReason == "" {
		t.Fatal("unknown kind should have a skip reason")
	}
	if s.Union.Total() != 0 {
		t.Fatalf("union = %+v, want empty", s.Union)
	}
}

func TestFingerprintFromArtifacts(t *testing.T) {
	t0 := time.Unix(1000, 0)
	arts := []Artifact{
		{Rel: "b.sarif", Size: 10, ModTime: t0},
		{Rel: "a.sarif", Size: 20, ModTime: t0},
	}
	fp1, err := fingerprintFromArtifacts(arts)
	if err != nil {
		t.Fatalf("fingerprintFromArtifacts: %v", err)
	}
	// Same input reordered must produce the same fingerprint.
	reordered := []Artifact{arts[1], arts[0]}
	fp2, err := fingerprintFromArtifacts(reordered)
	if err != nil {
		t.Fatalf("fingerprintFromArtifacts: %v", err)
	}
	if fp1 != fp2 {
		t.Fatal("fingerprint should be order-independent")
	}
	if fp1 == "" {
		t.Fatal("fingerprint should be non-empty")
	}
	// Changing a size changes the fingerprint.
	arts[0].Size = 11
	fp3, _ := fingerprintFromArtifacts(arts)
	if fp3 == fp1 {
		t.Fatal("fingerprint should change with size")
	}
	// Empty input produces a deterministic (sha256 of nothing) hash.
	fpEmpty, _ := fingerprintFromArtifacts(nil)
	if fpEmpty == "" || fpEmpty == fp1 {
		t.Fatal("empty fingerprint should be the hash of no parts")
	}
}
