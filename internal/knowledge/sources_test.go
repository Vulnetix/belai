package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func has(ix *Index, addr string) bool {
	for _, d := range ix.Docs() {
		if d.Address == addr {
			return true
		}
	}
	return false
}

func TestSyncProfileIndexesEligibleFilesOnly(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "handbook")
	write(t, filepath.Join(docs, "auth.md"), "Rotate the signing key every ninety days.")
	write(t, filepath.Join(docs, "nested", "ops.txt"), "Restore drills run quarterly.")
	write(t, filepath.Join(docs, ".hidden.md"), "hidden")
	write(t, filepath.Join(docs, "credentials.json"), `{"token":"x"}`)
	write(t, filepath.Join(docs, "image.png"), "\x89PNG\x00\x00")
	write(t, filepath.Join(docs, "blob.dat"), "binary\x00data")
	write(t, filepath.Join(docs, "node_modules", "dep", "x.md"), "dependency")
	outside := filepath.Join(root, "outside.md")
	write(t, outside, "outside secret")
	if err := os.Symlink(outside, filepath.Join(docs, "link.md")); err != nil {
		t.Skip("no symlinks")
	}
	single := filepath.Join(root, "single.md")
	write(t, single, "A single listed file about deployments.")

	ix := NewIndex("prof")
	st, err := SyncProfile(context.Background(), ix, "prof", []string{docs, single}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kb+prof/handbook/auth.md", "kb+prof/handbook/nested/ops.txt", "kb+prof/single.md"} {
		if !has(ix, want) {
			t.Fatalf("missing %s in %+v", want, ix.Docs())
		}
	}
	for _, d := range ix.Docs() {
		for _, bad := range []string{".hidden", "credentials", "image.png", "blob.dat", "node_modules", "link.md", "outside"} {
			if strings.Contains(d.Address, bad) {
				t.Fatalf("ineligible file indexed: %s", d.Address)
			}
		}
		if filepath.IsAbs(strings.TrimPrefix(d.Address, "kb+")) {
			t.Fatalf("an address must not be an absolute path: %s", d.Address)
		}
	}
	if st.Docs != 3 {
		t.Fatalf("stats = %+v", st)
	}
	hits := NewSet(ix).Search("signing key rotate", 0, nil)
	if len(hits) == 0 || hits[0].Address != "kb+prof/handbook/auth.md" || hits[0].Source != filepath.Join(docs, "auth.md") {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestSyncProfileRefusesSymlinkRootAndSkipsMissing(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	write(t, filepath.Join(real, "a.md"), "text about things")
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks")
	}
	ix := NewIndex("p")
	st, err := SyncProfile(context.Background(), ix, "p", []string{link, filepath.Join(root, "missing")}, nil, 0)
	if err != nil || len(ix.Docs()) != 0 || st.Skipped != 2 {
		t.Fatalf("symlink and missing must be skipped: %+v %v", st, err)
	}
}

func TestSyncProfileIsIncrementalAndDropsGoneFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "d", "a.md"), "alpha beta gamma")
	write(t, filepath.Join(root, "d", "b.md"), "delta epsilon zeta")
	ix := NewIndex("p")
	calls := 0
	gate := func(context.Context, string) (bool, error) { calls++; return true, nil }
	if _, err := SyncProfile(context.Background(), ix, "p", []string{filepath.Join(root, "d")}, gate, 0); err != nil {
		t.Fatal(err)
	}
	first := calls
	st, err := SyncProfile(context.Background(), ix, "p", []string{filepath.Join(root, "d")}, gate, 0)
	if err != nil || calls != first || st.Reused != 2 {
		t.Fatalf("second sync must reuse both: %+v calls %d/%d", st, calls, first)
	}
	if err := os.Remove(filepath.Join(root, "d", "b.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncProfile(context.Background(), ix, "p", []string{filepath.Join(root, "d")}, gate, 0); err != nil {
		t.Fatal(err)
	}
	if has(ix, "kb+p/d/b.md") || !has(ix, "kb+p/d/a.md") {
		t.Fatalf("docs = %+v", ix.Docs())
	}
}

func TestSyncProfileCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 6; i++ {
		write(t, filepath.Join(root, "d", fmt.Sprintf("f%d.md", i)), strings.Repeat("words about subject number one here\n", 60))
	}
	ix := NewIndex("p")
	st, err := SyncProfile(context.Background(), ix, "p", []string{filepath.Join(root, "d")}, nil, 500)
	if err != nil || !st.Truncated || ix.Tokens() > 500 {
		t.Fatalf("cap: %+v err=%v tokens=%d", st, err, ix.Tokens())
	}
}

func TestSyncProjectStructuredAndSkipsSecrets(t *testing.T) {
	root := t.TempDir()
	vx := filepath.Join(root, ".vulnetix")
	write(t, filepath.Join(vx, "sast.20260101000000.sarif"), `{"runs":[{"tool":{"driver":{"rules":[]}},"results":[
	 {"ruleId":"VNX-GO-SQLI","level":"error","message":{"text":"matched AKIAIOSFODNN7EXAMPLE"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"db/query.go"},"region":{"startLine":40}}}]},
	 {"ruleId":"VNX-GO-XSS","level":"warning","locations":[{"physicalLocation":{"artifactLocation":{"uri":"web/render.go"},"region":{"startLine":9}}}]}]}]}`)
	write(t, filepath.Join(vx, "inventory.cdx.json"), `{"components":[{"type":"library","name":"lodash","version":"4.17.20","purl":"pkg:npm/lodash@4.17.20"}],
	 "vulnerabilities":[{"id":"CVE-2021-23337","ratings":[{"severity":"high"}],"affects":[{"ref":"pkg:npm/lodash@4.17.20"}]}]}`)
	write(t, filepath.Join(vx, "vex.openvex.json"), `{"statements":[{"vulnerability":{"name":"CVE-2021-23337"},"products":[{"@id":"pkg:npm/lodash@4.17.20"}],"status":"not_affected","justification":"vulnerable_code_not_present"}]}`)
	write(t, filepath.Join(vx, "memory.yaml"), "project: example\nnotes: uses postgres for storage\n")
	write(t, filepath.Join(vx, "gitleaks-report.json"), `[{"Secret":"AKIAIOSFODNN7EXAMPLE","File":"x"}]`)
	write(t, filepath.Join(vx, "tool.log"), "log AKIAIOSFODNN7EXAMPLE")
	write(t, filepath.Join(vx, "belai", "state.json"), `{"private":"state"}`)
	write(t, filepath.Join(vx, "settings.json"), `{"api":"key"}`)

	ix := NewIndex("project")
	st, err := SyncProject(context.Background(), ix, root, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ix.Docs() {
		for _, bad := range []string{"gitleaks", "tool.log", "belai/", "settings.json"} {
			if strings.Contains(d.Address, bad) {
				t.Fatalf("must not index %s", d.Address)
			}
		}
	}
	if st.Docs != 4 {
		t.Fatalf("want sarif, cdx, vex and memory: %+v docs=%+v", st, ix.Docs())
	}
	for _, d := range ix.Docs() {
		if strings.HasSuffix(d.Address, ".sarif") && d.Chunks != 2 {
			t.Fatalf("sarif must be one chunk per result: %+v", d)
		}
	}
	s := NewSet(ix)
	hits := s.Search("sql injection rule VNX-GO-SQLI in query", 0, nil)
	if len(hits) == 0 || !strings.Contains(hits[0].Text, "db/query.go line 40") || hits[0].Start != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if h := s.Search("lodash 4.17.20 vulnerability", 0, nil); len(h) == 0 || !strings.Contains(h[0].Address, "inventory.cdx.json") {
		t.Fatalf("cdx hits = %+v", h)
	}
	for _, h := range s.Search("AKIAIOSFODNN7EXAMPLE secret key matched", 0, nil) {
		if strings.Contains(h.Text, "AKIA") {
			t.Fatalf("a secret reached the index: %+v", h)
		}
	}
}

func TestSyncProjectNoDirAndSymlinkDir(t *testing.T) {
	root := t.TempDir()
	ix := NewIndex("project")
	write(t, filepath.Join(root, "x"), "")
	if _, err := SyncProject(context.Background(), ix, root, nil, 0); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	write(t, filepath.Join(other, "memory.yaml"), "notes: outside")
	if err := os.Symlink(other, filepath.Join(root, ".vulnetix")); err != nil {
		t.Skip("no symlinks")
	}
	if _, err := SyncProject(context.Background(), ix, root, nil, 0); err == nil {
		t.Fatal("a symlinked .vulnetix must be refused")
	}
}

func TestSyncProjectDroppedDirectoryDropsChunks(t *testing.T) {
	root := t.TempDir()
	vx := filepath.Join(root, ".vulnetix")
	write(t, filepath.Join(vx, "memory.yaml"), "notes: uses postgres\n")
	ix := NewIndex("project")
	if _, err := SyncProject(context.Background(), ix, root, nil, 0); err != nil || len(ix.Docs()) != 1 {
		t.Fatalf("docs = %+v err=%v", ix.Docs(), err)
	}
	if err := os.RemoveAll(vx); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncProject(context.Background(), ix, root, nil, 0); err != nil || len(ix.Docs()) != 0 {
		t.Fatalf("a deleted .vulnetix must drop its chunks: %+v err=%v", ix.Docs(), err)
	}
}

func TestGateIsBatchedForManySmallChunks(t *testing.T) {
	ix := NewIndex("p")
	var chunks []RawChunk
	for i := 0; i < 600; i++ {
		chunks = append(chunks, RawChunk{Start: i + 1, End: i + 1, Text: fmt.Sprintf("record %d rule VNX-%d severity high", i, i)})
	}
	calls := 0
	gate := func(context.Context, string) (bool, error) { calls++; return true, nil }
	if _, err := ix.Ingest(context.Background(), Input{Address: "kb+p/a", Chunks: chunks}, gate, 0); err != nil {
		t.Fatal(err)
	}
	if calls >= 60 {
		t.Fatalf("a clean document of 600 small chunks took %d gate calls", calls)
	}
	// One flagged record is isolated and only it is dropped.
	chunks[300].Text = "BAD record"
	ix2 := NewIndex("p")
	bad := func(_ context.Context, s string) (bool, error) { return !strings.Contains(s, "BAD"), nil }
	r, err := ix2.Ingest(context.Background(), Input{Address: "kb+p/a", Chunks: chunks}, bad, 0)
	if err != nil || r.Dropped != 1 || r.Admitted != 599 {
		t.Fatalf("result %+v err=%v", r, err)
	}
}

func TestAddTextSessionFile(t *testing.T) {
	ix := NewIndex("session")
	if _, err := AddText(context.Background(), ix, "notes.md", "/work/notes.md", "The retry budget is three attempts with backoff.", 0); err != nil {
		t.Fatal(err)
	}
	h := NewSet(ix).Search("retry budget attempts", 0, nil)
	if len(h) == 0 || h[0].Address != "kb+session/notes.md" {
		t.Fatalf("hits = %+v", h)
	}
}

func TestAddressIsPlain(t *testing.T) {
	if got := Address("my profile:x", "../a/./b\x01c.md"); got != "kb+my-profile-x/a/b_c.md" {
		t.Fatalf("address = %q", got)
	}
	if !IsAddress("kb+project/x") || IsAddress("src/main.go") {
		t.Fatal("IsAddress")
	}
}
