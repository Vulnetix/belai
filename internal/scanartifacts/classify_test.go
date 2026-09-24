package scanartifacts

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		{"ai-bom.cdx.json", KindCycloneDXAIBOM, "", ""},
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

func TestClassifyExtendedPaths(t *testing.T) {
	cases := []struct {
		rel      string
		wantKind Kind
	}{
		{"scans/trivy.packages.json", KindPackagesScan},
		{"sub/dir/grype.log", KindToolLog},
		{"gitleaks-report.json", KindToolNative},
		{"semgrep.json", KindToolNative},
		{"trivy.json", KindToolNative},
		{"grype.json", KindToolNative},
		{"sub/signet/something.json", KindSignet},
		{"sub/plans/plan.json", KindSignet},
		{"sub/goals/goal.json", KindSignet},
		{"totally.unknown.file", KindUnknown},
		{"gitleaks.gz", KindToolLog},
		{"openvex.json", KindOpenVEX},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			kind, _, _, _ := Classify(tc.rel)
			if kind != tc.wantKind {
				t.Fatalf("Classify(%q) = %q, want %q", tc.rel, kind, tc.wantKind)
			}
		})
	}
}

func TestClassifyGzipSARIF(t *testing.T) {
	// A .sarif.gz basename still classifies as SARIF with its embedded tool.
	kind, tool, _, _ := Classify("sast.20260623212031.sarif.gz")
	if kind != KindSARIF || tool != "sast" {
		t.Fatalf("Classify(gz) = (%q, %q, _, _), want (sarif, sast, _, _)", kind, tool)
	}
}

func TestIsSignetPath(t *testing.T) {
	for _, p := range []string{
		"settings.json", "credentials.json",
		"code-review-summary.md", "code-review-manifest.json",
		"prompts.json", "sub/settings.json",
	} {
		if !isSignetPath(p) {
			t.Fatalf("isSignetPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"sbom.cdx.json", "sast.sarif", "notes.md"} {
		if isSignetPath(p) {
			t.Fatalf("isSignetPath(%q) = true, want false", p)
		}
	}
}

func TestIsSignetDir(t *testing.T) {
	for _, d := range []string{"signet", "signet/sub", "plans", "goals", "prompts", "prompts/x/y"} {
		if !isSignetDir(d) {
			t.Fatalf("isSignetDir(%q) = false, want true", d)
		}
	}
	for _, d := range []string{"", "scans", "other/signet"} {
		if isSignetDir(d) {
			t.Fatalf("isSignetDir(%q) = true, want false", d)
		}
	}
}

func TestKnownNative(t *testing.T) {
	for _, s := range []string{"gitleaks-report", "gitleaks-report.json", "semgrep-x", "trivy", "grype-123"} {
		if !knownNative(s) {
			t.Fatalf("knownNative(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"sast", "sbom", "random"} {
		if knownNative(s) {
			t.Fatalf("knownNative(%q) = true, want false", s)
		}
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		kind     Kind
		tool     string
		label    string
		contains string
	}{
		{KindCycloneDXSBOM, "", "", "software bill of materials"},
		{KindCycloneDXCBOM, "", "", "cryptographic bill of materials"},
		{KindCycloneDXAIBOM, "", "", "AI bill of materials"},
		{KindSARIF, "sast", "", "SARIF (sast)"},
		{KindSARIF, "sast", "branch", "SARIF (sast, branch)"},
		{KindOpenVEX, "", "", "status overlay"},
		{KindOpenVEXRiskAccepted, "", "", "risk-accepted overlay"},
		{KindMemory, "", "", "scan memory"},
		{KindCapabilities, "", "", "capability inventory"},
		{KindAnalyzeReport, "", "", "not parsed"},
		{KindToolLog, "", "", "tool diagnostics"},
		{KindToolNative, "", "", "tool-native output"},
		{KindUnknown, "", "", "unknown artifact"},
		{Kind("custom"), "", "", "custom"},
	}
	for _, tc := range cases {
		if got := describe(tc.kind, tc.tool, tc.label); !strings.Contains(got, tc.contains) {
			t.Fatalf("describe(%q, %q, %q) = %q, want substring %q", tc.kind, tc.tool, tc.label, got, tc.contains)
		}
	}
}

func TestParseSARIFStemBare(t *testing.T) {
	// No dots: no timestamp, no label.
	stamp, label := parseSARIFStem("sast")
	if !stamp.IsZero() || label != "" {
		t.Fatalf("parseSARIFStem(sast) = (%v, %q)", stamp, label)
	}
}

func TestParseSARIFStemDatePrefixLabel(t *testing.T) {
	stamp, label := parseSARIFStem("sast.20260805admin")
	if !stamp.IsZero() || label != "admin" {
		t.Fatalf("parseSARIFStem = (%v, %q), want (zero, admin)", stamp, label)
	}
}

func TestMarkSupersededModTimeOnly(t *testing.T) {
	dir := t.TempDir()
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	arts := []Artifact{
		{Path: filepath.Join(dir, "a.sarif"), Rel: "a.sarif", Kind: KindSARIF, Tool: "sast", ModTime: older},
		{Path: filepath.Join(dir, "b.sarif"), Rel: "b.sarif", Kind: KindSARIF, Tool: "sast", ModTime: newer},
	}
	markSuperseded(arts)
	if !arts[0].Superseded {
		t.Fatal("older artifact should be superseded")
	}
	if arts[1].Superseded {
		t.Fatal("newer artifact should be authoritative")
	}
}

func TestMarkSupersededStampWinsOverModTime(t *testing.T) {
	dir := t.TempDir()
	arts := []Artifact{
		{Path: filepath.Join(dir, "new.sarif"), Rel: "new.sarif", Kind: KindSARIF, Tool: "sast",
			ModTime: time.Now().Add(-time.Hour), Stamp: time.Now().Add(-time.Minute)},
		{Path: filepath.Join(dir, "old.sarif"), Rel: "old.sarif", Kind: KindSARIF, Tool: "sast",
			ModTime: time.Now(), Stamp: time.Now().Add(-2 * time.Hour)},
	}
	markSuperseded(arts)
	if arts[0].Superseded {
		t.Fatal("artifact with newer stamp should be authoritative even if modtime is older")
	}
	if !arts[1].Superseded {
		t.Fatal("artifact with older stamp should be superseded even if modtime is newer")
	}
}

func TestEnumerateSkipsSymlinkAndNonRegular(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sbom.cdx.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the dir should be skipped (not followed).
	target := filepath.Join(dir, "sbom.cdx.json")
	if err := os.Symlink(target, filepath.Join(dir, "link.cdx.json")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// A directory is also skipped.
	if err := os.MkdirAll(filepath.Join(dir, "scans"), 0o755); err != nil {
		t.Fatal(err)
	}
	arts, err := Enumerate(dir)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	for _, a := range arts {
		if a.Rel == "link.cdx.json" || a.Rel == "scans" {
			t.Fatalf("unexpected artifact %q in %v", a.Rel, arts)
		}
	}
	if len(arts) != 1 || arts[0].Rel != "sbom.cdx.json" {
		t.Fatalf("arts = %+v", arts)
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

// inventory.cdx.json is a deliberately distinct SBOM artifact produced by the
// sbom scanner; it must not be superseded as an older variant of sbom.cdx.json
// even though both classify as KindCycloneDXSBOM.
func TestInventorySBOMNotSupersededBySBOM(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	arts := []Artifact{
		{Path: filepath.Join(dir, "sbom.cdx.json"), Rel: "sbom.cdx.json", Kind: KindCycloneDXSBOM, Tool: "", ModTime: now},
		{Path: filepath.Join(dir, "inventory.cdx.json"), Rel: "inventory.cdx.json", Kind: KindCycloneDXSBOM, Tool: "", ModTime: now.Add(time.Second)},
	}
	markSuperseded(arts)
	if arts[0].Superseded || arts[1].Superseded {
		t.Fatal("inventory.cdx.json and sbom.cdx.json must both remain authoritative")
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
