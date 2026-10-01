package scanartifacts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecordsSARIFOnePerResultAndNoMessage(t *testing.T) {
	p := writeTemp(t, "a.sarif", `{"runs":[{"tool":{"driver":{"rules":[]}},"results":[
	 {"ruleId":"VNX-SECRET-001","level":"error","message":{"text":"found key AKIAIOSFODNN7EXAMPLE"},
	  "locations":[{"physicalLocation":{"artifactLocation":{"uri":"src/config.go"},"region":{"startLine":12,"snippet":{"text":"AKIAIOSFODNN7EXAMPLE"}}}}]},
	 {"ruleId":"VNX-GO-002","level":"warning","locations":[]},
	 {"ruleId":"GONE","level":"error","baselineState":"absent"}]}]}`)
	got, err := Records(context.Background(), KindSARIF, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %v", got)
	}
	if !strings.Contains(got[0], "VNX-SECRET-001") || !strings.Contains(got[0], "src/config.go line 12") {
		t.Fatalf("record = %q", got[0])
	}
	for _, r := range got {
		if strings.Contains(r, "AKIA") {
			t.Fatalf("a matched secret reached a record: %q", r)
		}
	}
}

func TestRecordsCycloneDXComponentsAndVulns(t *testing.T) {
	p := writeTemp(t, "a.cdx.json", `{"bomFormat":"CycloneDX","metadata":{"properties":[]},
	"components":[{"type":"library","name":"lodash","version":"4.17.20","purl":"pkg:npm/lodash@4.17.20"},{"name":""}],
	"dependencies":[{"ref":"x"}],
	"vulnerabilities":[{"id":"CVE-2021-23337","ratings":[{"severity":"high","score":7.2}],"affects":[{"ref":"pkg:npm/lodash@4.17.20"}],"description":"free text advisory"}]}`)
	got, err := Records(context.Background(), KindCycloneDXSBOM, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("records = %v", got)
	}
	if !strings.Contains(got[0], "lodash version 4.17.20") || !strings.Contains(got[1], "CVE-2021-23337") {
		t.Fatalf("records = %v", got)
	}
	for _, r := range got {
		if strings.Contains(r, "free text") {
			t.Fatalf("advisory prose reached a record: %q", r)
		}
	}
}

func TestRecordsOpenVEXAndOtherKinds(t *testing.T) {
	p := writeTemp(t, "a.openvex.json", `{"statements":[{"vulnerability":{"name":"CVE-2024-1"},"products":[{"@id":"pkg:npm/a@1"}],"status":"not_affected","justification":"vulnerable_code_not_present","impact_statement":"free text"}]}`)
	got, err := Records(context.Background(), KindOpenVEX, p, 0)
	if err != nil || len(got) != 1 || !strings.Contains(got[0], "not_affected") || !strings.Contains(got[0], "vulnerable_code_not_present") || strings.Contains(got[0], "free text") {
		t.Fatalf("records = %v err=%v", got, err)
	}
	if got, err := Records(context.Background(), KindMemory, p, 0); got != nil || err != nil {
		t.Fatalf("other kinds give nothing: %v %v", got, err)
	}
}

func TestRecordsMalformedIsAnError(t *testing.T) {
	p := writeTemp(t, "bad.sarif", `{not json`)
	if _, err := Records(context.Background(), KindSARIF, p, 0); err == nil {
		t.Fatal("malformed input must be an error")
	}
}
