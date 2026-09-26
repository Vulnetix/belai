package scanartifacts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFact(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A CBOM is an inventory: its facts are algorithms and their post-quantum
// status, not findings.
func TestCycloneDXFactsCBOM(t *testing.T) {
	p := writeFact(t, "cbom.cdx.json", `{"bomFormat":"CycloneDX","metadata":{"properties":[
		{"name":"vulnetix:cbom/algorithms-detected","value":"3"},
		{"name":"vulnetix:cbom/deprecated","value":"1"},
		{"name":"vulnetix:env/hostname","value":"box"}]},
	"components":[
		{"type":"cryptographic-asset","name":"HMAC","properties":[{"name":"vulnetix:crypto/category","value":"algorithm"},{"name":"vulnetix:crypto/pqc-status","value":"quantum-safe"}]},
		{"type":"cryptographic-asset","name":"SHA-1","properties":[{"name":"vulnetix:crypto/category","value":"algorithm"},{"name":"vulnetix:crypto/pqc-status","value":"deprecated"}]},
		{"type":"library","name":"Node crypto","purl":"pkg:npm/crypto","properties":[{"name":"vulnetix:crypto/category","value":"library"}]}
	],"dependencies":[{"ref":"x"}]}`)
	f, err := CycloneDXFacts(context.Background(), p, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := f.MetaInt("vulnetix:cbom/algorithms-detected"); !ok || n != 3 {
		t.Fatalf("algorithms-detected = %d, %v", n, ok)
	}
	if _, ok := f.Metadata["vulnetix:env/hostname"]; ok {
		t.Fatal("host facts must not be kept")
	}
	if f.Components != 3 || f.Categories["algorithm"] != 2 || f.Ecosystems["npm"] != 1 {
		t.Fatalf("facts = %+v", f)
	}
	if f.PQCCounts["deprecated"] != 1 || len(f.PQC["deprecated"]) != 1 || f.PQC["deprecated"][0] != "SHA-1" {
		t.Fatalf("pqc = %v %v", f.PQCCounts, f.PQC)
	}
}

// An SCA SBOM splits real vulnerabilities from license issues.
func TestCycloneDXFactsSCA(t *testing.T) {
	p := writeFact(t, "sbom.cdx.json", `{"components":[
		{"type":"library","name":"a","purl":"pkg:golang/a@1"},
		{"type":"library","name":"b","purl":"pkg:npm/b@1"},
		{"type":"library","name":"c","purl":"pkg:npm/c@1"}],
	"vulnerabilities":[
		{"id":"CVE-2026-0001","ratings":[{"source":{"name":"NVD"},"score":9.8,"severity":"critical","method":"CVSSv31"}]},
		{"id":"LICENSE:x","source":{"name":"vulnetix-license-analyzer"},"properties":[{"name":"vulnetix:license-severity","value":"medium"}]}]}`)
	f, err := CycloneDXFacts(context.Background(), p, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if f.Vulns.Critical != 1 || f.Vulns.Total() != 1 || f.Licenses != 1 {
		t.Fatalf("vulns %+v licenses %d", f.Vulns, f.Licenses)
	}
	top := f.TopEcosystems()
	if len(top) != 2 || top[0] != (Tally{"npm", 2}) || top[1] != (Tally{"golang", 1}) {
		t.Fatalf("ecosystems = %v", top)
	}
}

// SARIF facts count fired rules against evaluated ones and keep malscan's
// scan properties.
func TestSARIFFacts(t *testing.T) {
	p := writeFact(t, "sast.sarif", `{"runs":[{"tool":{"driver":{"rules":[{"id":"R1"},{"id":"R2"},{"id":"R3"}]}},"results":[
		{"ruleId":"R1","level":"error"},{"ruleId":"R1","level":"warning"},{"ruleId":"R2","level":"note"},
		{"ruleId":"R3","level":"error","suppressions":[{}]}]}]}`)
	r, err := SARIFFacts(context.Background(), p, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if r.Results != 3 || r.RulesTriggered != 2 || r.RulesEvaluated != 3 || r.Counts.Total() != 3 {
		t.Fatalf("facts = %+v", r)
	}

	m := writeFact(t, "malscan.sarif", `{"runs":[{"properties":{"filesScanned":3,"indicatorCount":246709,"malicious":false},"results":[],"tool":{"driver":{"name":"malscan"}}}]}`)
	r, err = SARIFFacts(context.Background(), m, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if r.FilesScanned != 3 || r.Indicators != 246709 || r.Malicious || r.Results != 0 {
		t.Fatalf("malscan facts = %+v", r)
	}
}
