package knowledge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const oneResult = `{"runs":[{"tool":{"driver":{"rules":[]}},"results":[{"ruleId":"VNX-GO-SQLI","level":"error","locations":[{"physicalLocation":{"artifactLocation":{"uri":"db/query.go"},"region":{"startLine":40}}}]}]}]}`

// Only the newest copy of a repeated scan is indexed; a newer scan replaces the
// older one at the next refresh, so a model never reads stale findings beside
// the current ones.
func TestSyncProjectIndexesOnlyTheNewestCopyOfARepeatedScan(t *testing.T) {
	root := t.TempDir()
	vx := filepath.Join(root, ".vulnetix")
	write(t, filepath.Join(vx, "sast.20260101000000.sarif"), oneResult)
	write(t, filepath.Join(vx, "sast.20260201000000.sarif"), oneResult)

	ix := NewIndex("project")
	if _, err := SyncProject(context.Background(), ix, root, nil, 0); err != nil {
		t.Fatal(err)
	}
	var sarifs []string
	for _, d := range ix.Docs() {
		if strings.HasSuffix(d.Address, ".sarif") {
			sarifs = append(sarifs, d.Address)
		}
	}
	if len(sarifs) != 1 || !strings.Contains(sarifs[0], "20260201000000") {
		t.Fatalf("indexed %v, want only the newest scan", sarifs)
	}

	// A scan run a moment later takes over from it at the next refresh.
	write(t, filepath.Join(vx, "sast.20260301000000.sarif"), oneResult)
	if _, err := SyncProject(context.Background(), ix, root, nil, 0); err != nil {
		t.Fatal(err)
	}
	if has(ix, Address(ProjectScope, ".vulnetix/sast.20260201000000.sarif")) {
		t.Fatal("the superseded scan stayed in the index")
	}
	if !has(ix, Address(ProjectScope, ".vulnetix/sast.20260301000000.sarif")) {
		t.Fatal("the new scan was not indexed")
	}
}
