package scanartifacts

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestClassifyKnownArtifacts(t *testing.T) {
	cases := []struct {
		rel       string
		wantKind  Kind
		wantTool  string
		wantLabel string
	}{
		{"sbom.cdx.json", KindCycloneDXSBOM, "", ""},
		{"cbom.cdx.json", KindCycloneDXCBOM, "", ""},
		{"aibom.cdx.json", KindCycloneDXAIBOM, "", ""},
		{"sast.sarif", KindSARIF, "sast", ""},
		{"sast.20260623212031.sarif", KindSARIF, "sast", ""},
		{"sast.20260805-admin-reviews.sarif", KindSARIF, "sast", "-admin-reviews"},
		{"vex.json", KindOpenVEX, "", ""},
		{"vex-risk-accepted.json", KindOpenVEXRiskAccepted, "", ""},
		{"memory.yaml", KindMemory, "", ""},
		{"capabilities.yaml", KindCapabilities, "", ""},
		{"analyze.report.json", KindAnalyzeReport, "", ""},
		{"scan.stderr", KindToolLog, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			kind, tool, _, label := Classify(tc.rel)
			if kind != tc.wantKind || tool != tc.wantTool || label != tc.wantLabel {
				t.Fatalf("Classify(%q) = (%q, %q, _, %q), want (%q, %q, _, %q)", tc.rel, kind, tool, label, tc.wantKind, tc.wantTool, tc.wantLabel)
			}
		})
	}
}

func TestEnumerateSkipsSignetFiles(t *testing.T) {
	dir := t.TempDir()
	mkdir := func(p string) {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("signet")
	mkdir("prompts")
	write("settings.json", "{}")
	write("prompts.json", "{}")
	write("signet/credentials.json", "{}")
	write("prompts/010-deploy.md", "deploy")
	write("sbom.cdx.json", `{}`)
	write("memory.yaml", "last_scan:\n")

	arts, err := Enumerate(dir)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	rels := make([]string, len(arts))
	for i, a := range arts {
		rels[i] = a.Rel
	}
	for _, bad := range []string{"settings.json", "prompts.json", "signet/credentials.json", "prompts/010-deploy.md"} {
		if slices.Contains(rels, bad) {
			t.Fatalf("signet file %q should be skipped", bad)
		}
	}
	if !slices.Contains(rels, "sbom.cdx.json") {
		t.Fatalf("expected sbom.cdx.json in %v", rels)
	}
}

func TestMarkSuperseded(t *testing.T) {
	dir := t.TempDir()
	arts := []Artifact{
		{Path: filepath.Join(dir, "sast.sarif"), Rel: "sast.sarif", Kind: KindSARIF, Tool: "sast", ModTime: time.Now().Add(-time.Hour)},
		{Path: filepath.Join(dir, "sast.20260623212031.sarif"), Rel: "sast.20260623212031.sarif", Kind: KindSARIF, Tool: "sast", Stamp: time.Date(2026, 6, 23, 21, 20, 31, 0, time.UTC)},
	}
	markSuperseded(arts)
	if !arts[0].Superseded {
		t.Fatal("older sast.sarif should be superseded")
	}
	if arts[1].Superseded {
		t.Fatal("newer timestamped SARIF should be authoritative")
	}
}
