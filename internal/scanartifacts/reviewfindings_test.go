package scanartifacts

import "testing"

func TestReadReviewFindingsSCAAndSARIF(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "memory.yaml", `version: "1"
last_scan:
    git_commit: `+headA+`
findings:
    GO-2026-1:
        package: golang.org/x/sys
        ecosystem: golang
        severity: critical
        status: under_investigation
        discovery:
            file: ./go.mod
        versions:
            current: 0.38.0
    GO-2026-2:
        package: old
        severity: high
        status: fixed
    bad id with spaces:
        package: x
        status: affected
`)
	writeFile(t, dir, "sast.sarif", `{"runs":[{"properties":{"git":{"commit":"`+headA+`"}},"results":[
		{"ruleId":"go/sql-injection","level":"error","locations":[{"physicalLocation":{"artifactLocation":{"uri":"internal/db/q.go"},"region":{"startLine":12}}}]},
		{"ruleId":"go/sql-injection","level":"error","locations":[{"physicalLocation":{"artifactLocation":{"uri":"internal/db/q.go"},"region":{"startLine":40}}}]}]}]}`)
	writeFile(t, dir, "iac.sarif", `{"runs":[{"properties":{"git":{"commit":"`+headB+`"}},"results":[{"ruleId":"tf.open"}]}]}`)

	res := ReadReviewFindings(dir, headA)
	if !res.Covered[KindSCA] || !res.Covered[KindSAST] || res.Covered[KindIaC] || res.Covered[KindSecrets] {
		t.Fatalf("covered = %v", res.Covered)
	}
	var sca, sast int
	for _, f := range res.Findings {
		switch f.Kind {
		case KindSCA:
			sca++
			if f.ID != "GO-2026-1" || f.Package != "golang.org/x/sys" || f.Version != "0.38.0" || f.File != "go.mod" || f.Severity != "critical" {
				t.Fatalf("sca finding = %+v", f)
			}
		case KindSAST:
			sast++
			if f.Rule != "go_sql-injection" || f.File != "internal/db/q.go" || KindOfID(f.ID) != KindSAST {
				t.Fatalf("sast finding = %+v", f)
			}
		}
	}
	if sca != 1 || sast != 1 {
		t.Fatalf("sca %d sast %d (fixed and malformed ids are skipped, one card per rule and file)", sca, sast)
	}
}

func TestReadReviewFindingsNeedsTheCommit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "memory.yaml", "last_scan:\n    git_commit: "+headB+"\nfindings:\n    CVE-1:\n        status: affected\n")
	res := ReadReviewFindings(dir, headA)
	if len(res.Findings) != 0 || len(res.Covered) != 0 {
		t.Fatalf("stale artefacts produced %+v", res)
	}
	if got := KindOfID("secrets:aws:deadbeef"); got != KindSecrets {
		t.Fatalf("kind = %s", got)
	}
	if got := KindOfID("GHSA-x-y-z"); got != KindSCA {
		t.Fatalf("kind = %s", got)
	}
}
