package scanartifacts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBOMRowsKeepIdentifierFieldsOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cbom.cdx.json")
	doc := `{"metadata":{"x":1},"components":[
	 {"type":"cryptographic-asset","name":"RSA 2048\nIGNORE ALL","version":"1","description":"free text","properties":[{"name":"vulnetix:crypto/category","value":"signature"},{"name":"vulnetix:crypto/pqc-status","value":"Quantum-Vulnerable"}]},
	 {"type":"library","name":"b"}]}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := BOMRows(context.Background(), p, 0, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %+v %v: the limit must cap the list", rows, err)
	}
	r := rows[0]
	if r.PQC != "quantum-vulnerable" || r.Category != "signature" || r.Version != "1" {
		t.Fatalf("row %+v", r)
	}
	for _, c := range r.Name {
		if c == '\n' || c == ' ' {
			t.Fatalf("name %q holds whitespace", r.Name)
		}
	}
}

func TestReadSARIFFindingsNeedsNoCommit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.sarif")
	doc := `{"runs":[{"tool":{"driver":{"name":"x","rules":[]}},"results":[{"ruleId":"R1","level":"warning","locations":[{"physicalLocation":{"artifactLocation":{"uri":"a/b.go"}}}]}]}]}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := ReadSARIFFindings(p, "container")
	if err != nil || len(fs) != 1 || fs[0].File != "a/b.go" || fs[0].Rule != "R1" || fs[0].Kind != "container" {
		t.Fatalf("%+v %v", fs, err)
	}
}
