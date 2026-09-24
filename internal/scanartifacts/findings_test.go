package scanartifacts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderFindingLines(t *testing.T) {
	long := ""
	for i := 0; i < 250; i++ {
		long += "x"
	}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"trims blank and whitespace",
			[]string{"  a  ", "", "   ", "b"},
			[]string{"a", "b"},
		},
		{
			"clips over-long line",
			[]string{long},
			[]string{long[:maxFindingLineChars] + "…"},
		},
		{
			"caps total lines",
			func() []string {
				var in []string
				for i := 0; i < maxFindingLines+10; i++ {
					in = append(in, "line")
				}
				return in
			}(),
			func() []string {
				var out []string
				for i := 0; i < maxFindingLines; i++ {
					out = append(out, "line")
				}
				return out
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderFindingLines(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.want))
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestFirstSARIFLocation(t *testing.T) {
	withLoc := sarifResult{
		Locations: []struct {
			PhysicalLocation struct {
				ArtifactLocation struct {
					URI string `json:"uri"`
				} `json:"artifactLocation"`
				Region struct {
					StartLine int `json:"startLine"`
				} `json:"region"`
			} `json:"physicalLocation"`
		}{
			{
				PhysicalLocation: struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				}{
					ArtifactLocation: struct {
						URI string `json:"uri"`
					}{URI: "src/main.go"},
					Region: struct {
						StartLine int `json:"startLine"`
					}{StartLine: 42},
				},
			},
		},
	}
	uri, line := firstSARIFLocation(withLoc)
	if uri != "src/main.go" || line != 42 {
		t.Fatalf("firstSARIFLocation = (%q, %d), want (src/main.go, 42)", uri, line)
	}

	uri, line = firstSARIFLocation(sarifResult{})
	if uri != "" || line != 0 {
		t.Fatalf("empty result = (%q, %d), want (\"\", 0)", uri, line)
	}
}

func TestSARIFFindingLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sast.sarif")
	doc := `{
  "runs": [{
    "tool": {"driver": {"rules": [
      {"id": "VNX-001", "properties": {"severity": "critical"}}
    ]}},
    "results": [
      {"ruleId": "VNX-001", "locations": [{"physicalLocation": {"artifactLocation": {"uri": "a.go"}, "region": {"startLine": 1}}}]},
      {"ruleId": "VNX-002", "level": "error", "locations": [{"physicalLocation": {"artifactLocation": {"uri": "b.go"}, "region": {"startLine": 2}}}]},
      {"ruleId": "VNX-003", "level": "error", "baselineState": "absent"},
      {"ruleId": "VNX-004", "level": "error", "suppressions": [{}]}
    ]
  }]
}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := SARIFFindingLines(context.Background(), path, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("SARIFFindingLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d (%v), want 2 (absent and suppressed skipped)", len(lines), lines)
	}
	if lines[0] != "VNX-001 critical a.go:1" {
		t.Fatalf("lines[0] = %q", lines[0])
	}
	if lines[1] != "VNX-002 high b.go:2" {
		t.Fatalf("lines[1] = %q", lines[1])
	}
}

func TestSARIFFindingLinesMissing(t *testing.T) {
	lines, err := SARIFFindingLines(context.Background(), filepath.Join(t.TempDir(), "nope.sarif"), DefaultMaxBytes)
	if err == nil || lines != nil {
		t.Fatalf("expected error for missing file, got lines=%v err=%v", lines, err)
	}
}

func TestCycloneDXFindingLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sbom.cdx.json")
	doc := `{"vulnerabilities": [
	  {"id": "CVE-1", "source": {"name": "NVD"}, "ratings": [{"score": 9.0, "method": "CVSSv31", "source": {"name": "NVD"}}], "affects": [{"ref": "pkg:a"}, {"ref": "pkg:b"}]},
	  {"id": "LICENSE-1", "source": {"name": "vulnetix-license-analyzer"}, "properties": [{"name": "vulnetix:license-severity", "value": "high"}]},
	  {"id": "CVE-2", "source": {"name": "NVD"}, "ratings": [{"score": 3.0, "method": "CVSSv31", "source": {"name": "NVD"}}]}
	]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := CycloneDXFindingLines(context.Background(), path, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("CycloneDXFindingLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d (%v), want 2 (license skipped)", len(lines), lines)
	}
	if lines[0] != "CVE-1 critical pkg:a,pkg:b" {
		t.Fatalf("lines[0] = %q", lines[0])
	}
	if lines[1] != "CVE-2 low" {
		t.Fatalf("lines[1] = %q", lines[1])
	}
}

func TestOpenVEXFindingLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vex.json")
	doc := `{"statements": [
	  {"vulnerability": {"name": "CVE-1"}, "status": "affected", "impact_statement": "Severity: high"},
	  {"vulnerability": {"name": "CVE-2"}, "status": "fixed"}
	]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := OpenVEXFindingLines(context.Background(), path, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("OpenVEXFindingLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d (%v), want 2", len(lines), lines)
	}
	if lines[0] != "CVE-1 affected Severity: high" {
		t.Fatalf("lines[0] = %q", lines[0])
	}
	if lines[1] != "CVE-2 fixed -" {
		t.Fatalf("lines[1] = %q", lines[1])
	}
}
