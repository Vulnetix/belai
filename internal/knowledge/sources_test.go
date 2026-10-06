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
	st, err := SyncProfile(context.Background(), ix, "prof", "", []string{docs, single}, nil, nil, 0)
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
	st, err := SyncProfile(context.Background(), ix, "p", "", []string{link, filepath.Join(root, "missing")}, nil, nil, 0)
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
	if _, err := SyncProfile(context.Background(), ix, "p", "", []string{filepath.Join(root, "d")}, gate, nil, 0); err != nil {
		t.Fatal(err)
	}
	first := calls
	st, err := SyncProfile(context.Background(), ix, "p", "", []string{filepath.Join(root, "d")}, gate, nil, 0)
	if err != nil || calls != first || st.Reused != 2 {
		t.Fatalf("second sync must reuse both: %+v calls %d/%d", st, calls, first)
	}
	if err := os.Remove(filepath.Join(root, "d", "b.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncProfile(context.Background(), ix, "p", "", []string{filepath.Join(root, "d")}, gate, nil, 0); err != nil {
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
	st, err := SyncProfile(context.Background(), ix, "p", "", []string{filepath.Join(root, "d")}, nil, nil, 500)
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

func TestSyncProfileRelativePathsResolveUnderTheRootAndCannotLeaveIt(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "docs", "guide.md"), "Rotate the signing key every ninety days.")
	write(t, filepath.Join(outside, "secret.md"), "outside the repository")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip("no symlinks")
	}
	write(t, filepath.Join(root, "real", "x.md"), "inside")
	if err := os.Symlink(filepath.Join(outside), filepath.Join(root, "real", "up")); err != nil {
		t.Skip("no symlinks")
	}
	ix := NewIndex("p")
	st, err := SyncProfile(context.Background(), ix, "p", root, []string{"docs", "linked", "real/up", "missing"}, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !has(ix, "kb+p/docs/guide.md") {
		t.Fatalf("a relative path resolves under the root: %+v", ix.Docs())
	}
	for _, d := range ix.Docs() {
		if strings.Contains(d.Address, "secret") || strings.Contains(d.Address, "linked") || strings.Contains(d.Address, "up/") {
			t.Fatalf("a relative path led out of the root: %s", d.Address)
		}
	}
	if st.Skipped != 3 {
		t.Fatalf("symlink, nested symlink and missing are skipped: %+v", st)
	}
	// With no root a relative path names nothing.
	ix2 := NewIndex("p")
	if st, _ := SyncProfile(context.Background(), ix2, "p", "", []string{"docs"}, nil, nil, 0); len(ix2.Docs()) != 0 || st.Skipped != 1 {
		t.Fatalf("no root: %+v %+v", st, ix2.Docs())
	}
}

func TestSyncProfileVulnetixIsReadAsScannerOutput(t *testing.T) {
	root := t.TempDir()
	vx := filepath.Join(root, ".vulnetix")
	write(t, filepath.Join(vx, "sast.20260101000000.sarif"), `{"runs":[{"tool":{"driver":{"rules":[]}},"results":[
	 {"ruleId":"VNX-GO-SQLI","level":"error","message":{"text":"matched AKIAIOSFODNN7EXAMPLE"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"db/query.go"},"region":{"startLine":40}}}]}]}]}`)
	write(t, filepath.Join(vx, "gitleaks-report.json"), `[{"Secret":"AKIAIOSFODNN7EXAMPLE"}]`)
	write(t, filepath.Join(vx, "memory.yaml"), "notes: uses postgres\n")

	var profileCalls, scannerCalls int
	profileGate := func(context.Context, string) (bool, error) { profileCalls++; return true, nil }
	scannerGate := func(context.Context, string) (bool, error) { scannerCalls++; return true, nil }
	ix := NewIndex("scout")
	st, err := SyncProfile(context.Background(), ix, "scout", root, []string{".vulnetix"}, profileGate, scannerGate, 0)
	if err != nil || st.Docs != 2 {
		t.Fatalf("want the sarif and memory file: %+v err=%v docs=%+v", st, err, ix.Docs())
	}
	if profileCalls != 0 || scannerCalls == 0 {
		t.Fatalf("scanner output is third-party text and goes through the scanner gate: profile=%d scanner=%d", profileCalls, scannerCalls)
	}
	for _, d := range ix.Docs() {
		if strings.Contains(d.Address, "gitleaks") {
			t.Fatalf("a native third-party report was indexed: %s", d.Address)
		}
		if !strings.HasPrefix(d.Address, "kb+scout/.vulnetix/") {
			t.Fatalf("address = %s", d.Address)
		}
	}
	h := NewSet(ix).Search("VNX-GO-SQLI query injection", 0, nil)
	if len(h) == 0 || !strings.Contains(h[0].Text, "db/query.go line 40") || strings.Contains(h[0].Text, "AKIA") {
		t.Fatalf("hits = %+v", h)
	}
}

// A profile that lists .vulnetix and the project index both hold the artifacts:
// a search or a glob shows each passage once.
func TestAPassageInTheProfileAndTheProjectIsShownOnce(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".vulnetix", "memory.yaml"), "notes: the service stores sessions in postgres\n")
	prof, proj := NewIndex("scout"), NewIndex(ProjectScope)
	if _, err := SyncProfile(context.Background(), prof, "scout", root, []string{".vulnetix"}, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncProject(context.Background(), proj, root, nil, 0); err != nil {
		t.Fatal(err)
	}
	set := NewSet(prof, proj)
	if h := set.Search("sessions postgres storage", 0, nil); len(h) != 1 {
		t.Fatalf("the same passage twice: %+v", h)
	}
	if m := set.Match(func(string, string) bool { return true }); len(m) != 1 {
		t.Fatalf("the same document twice: %v", m)
	}
}

func TestEnumerateProfileGivesEachFileAnAddressAndADestination(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(outside, "handbook", "keys.md"), "Rotate keys.")
	write(t, filepath.Join(outside, "single.md"), "A single file.")
	write(t, filepath.Join(root, "docs", "guide.md"), "A guide.")
	write(t, filepath.Join(root, ".vulnetix", "memory.yaml"), "notes: uses postgres\n")
	write(t, filepath.Join(root, ".vulnetix", "gitleaks-report.json"), `[{"Secret":"AKIA"}]`)
	files, skipped, err := EnumerateProfile(context.Background(), root, []string{filepath.Join(outside, "handbook"), filepath.Join(outside, "single.md"), "docs", ".vulnetix", "missing"})
	if err != nil || skipped != 1 {
		t.Fatalf("skipped=%d err=%v files=%+v", skipped, err, files)
	}
	got := map[string]ProfileFile{}
	for _, f := range files {
		got[f.Rel] = f
	}
	for rel, want := range map[string]string{
		"handbook/keys.md":      ".vulnetix/knowledge/handbook/keys.md",
		"single.md":             ".vulnetix/knowledge/single.md",
		"docs/guide.md":         "docs/guide.md",
		".vulnetix/memory.yaml": ".vulnetix/memory.yaml",
	} {
		f, ok := got[rel]
		if !ok || f.Dest != want {
			t.Fatalf("%s: %+v (want dest %s) in %+v", rel, f, want, files)
		}
	}
	if got[".vulnetix/memory.yaml"].Artifact == nil || got["docs/guide.md"].Artifact != nil {
		t.Fatal("only scanner output is an artefact")
	}
	if _, ok := got[".vulnetix/gitleaks-report.json"]; ok {
		t.Fatal("a native third-party report must not be listed")
	}
	if !got["docs/guide.md"].Relative || got["single.md"].Relative {
		t.Fatal("Relative says whether the listed path was relative")
	}
}

func TestBlockedAbsoluteRefusesCredentialStoresAndSystemPlaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BELAI_HOME", filepath.Join(home, "state"))
	for _, p := range []string{"/", "/etc/shadow", "/etc/ssh/sshd_config", "/proc/1/environ", home, filepath.Join(home, ".ssh"), filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".aws"), filepath.Join(home, ".config", "gcloud", "x"), filepath.Join(home, "state"), filepath.Join(home, "state", "kanban")} {
		if !BlockedAbsolute(p) {
			t.Errorf("%s must be blocked", p)
		}
	}
	for _, p := range []string{filepath.Join(home, "handbook"), filepath.Join(home, "docs", "a.md"), "/srv/standards", "/usr/share/doc", filepath.Join(home, ".config", "myapp"),
		"/root/handbook", "/etc/nginx/conf.d", "/boot/docs"} {
		if BlockedAbsolute(p) {
			t.Errorf("%s is an ordinary place", p)
		}
	}
}

func TestEnumerateProfileSkipsTheFloorAndSymlinks(t *testing.T) {
	home, root, outside := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	write(t, filepath.Join(home, ".ssh", "config"), "Host x\n")
	write(t, filepath.Join(home, "handbook", "a.md"), "A handbook.")
	write(t, filepath.Join(outside, "secret.md"), "outside")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip("no symlinks")
	}
	files, skipped, err := EnumerateProfile(context.Background(), root, []string{"~/.ssh", "~", home, "linked", ".git", "sub/.git/hooks", "~/handbook"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Rel != "handbook/a.md" || skipped != 6 {
		t.Fatalf("only the ordinary document is listed: skipped=%d files=%+v", skipped, files)
	}
}
