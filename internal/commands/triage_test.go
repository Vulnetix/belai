package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildTriageBlocksRendersFindings(t *testing.T) {
	workdir := t.TempDir()
	vdir := filepath.Join(workdir, ".vulnetix")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	sarif := `{"runs":[{"tool":{"driver":{"rules":[]}},"results":[
		{"ruleId":"S1","level":"error","locations":[{"physicalLocation":{"artifactLocation":{"uri":"internal/a.go"},"region":{"startLine":12}}}]}
	]}]}`
	if err := os.WriteFile(filepath.Join(vdir, "sast.sarif"), []byte(sarif), 0o600); err != nil {
		t.Fatal(err)
	}
	cdx := `{"vulnerabilities":[{"id":"CVE-2026-0001","source":{"name":"NVD"},"ratings":[{"source":{"name":"NVD"},"score":9.8,"severity":"critical","method":"CVSSv31"}],"affects":[{"ref":"pkg:golang/example"}]}]}`
	if err := os.WriteFile(filepath.Join(vdir, "sbom.cdx.json"), []byte(cdx), 0o600); err != nil {
		t.Fatal(err)
	}

	blocks := BuildTriageBlocks(context.Background(), workdir)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	var sawSARIF, sawSBOM bool
	for _, b := range blocks {
		switch {
		case b.Label == "sast report" && strings.Contains(b.Body, "S1") && strings.Contains(b.Body, "internal/a.go:12"):
			sawSARIF = true
		case b.Label == "sbom report" && strings.Contains(b.Body, "CVE-2026-0001"):
			sawSBOM = true
		}
	}
	if !sawSARIF || !sawSBOM {
		t.Fatalf("triage blocks did not carry the findings: %+v", blocks)
	}
}
