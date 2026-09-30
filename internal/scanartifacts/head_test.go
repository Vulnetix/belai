package scanartifacts

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	headA = "f6808a50403cccea6f4e73c7bbbcc11395b65e50"
	headB = "0123456789abcdef0123456789abcdef01234567"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReviewedAtEachSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "memory.yaml", "version: \"1\"\nlast_scan:\n    timestamp: \"x\"\n    git_commit: "+headA+"\n    packages: 1\nhistory: []\n")
	writeFile(t, dir, "sbom.cdx.json", `{"metadata":{"component":{"properties":[{"name":"vulnetix:git/commit","value":"`+headA+`"}]}}}`)
	writeFile(t, dir, "malscan.sarif", `{"runs":[{"properties":{"git":{"commit":"`+headA+`","dirty":true}}}]}`)
	writeFile(t, dir, "other.cdx.json", `{"metadata":{"component":{"properties":[{"name":"vulnetix:git/commit","value":"`+headB+`"}]}}}`)

	ok, ev := ReviewedAt(dir, headA)
	if !ok {
		t.Fatal("no evidence for a commit three artefacts record")
	}
	want := []string{"malscan.sarif", "memory.yaml", "sbom.cdx.json"}
	if len(ev.Files) != len(want) {
		t.Fatalf("files = %v, want %v", ev.Files, want)
	}
	for i := range want {
		if ev.Files[i] != want[i] {
			t.Fatalf("files = %v, want %v", ev.Files, want)
		}
	}
}

func TestReviewedAtMismatchAndMalformed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "memory.yaml", "last_scan:\n    git_commit: "+headB+"\n")
	writeFile(t, dir, "sbom.cdx.json", `not json`)
	if ok, _ := ReviewedAt(dir, headA); ok {
		t.Fatal("an older commit is not evidence for HEAD")
	}
	for _, head := range []string{"", "HEAD", "f6808a5", headA[:39]} {
		if ok, _ := ReviewedAt(dir, head); ok {
			t.Fatalf("head %q must never match", head)
		}
	}
	if ok, _ := ReviewedAt(filepath.Join(dir, "absent"), headA); ok {
		t.Fatal("a missing directory is not evidence")
	}
}

func TestReviewedAtIgnoresCase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "malscan.sarif", `{"runs":[{"properties":{"git":{"commit":"F6808A50403CCCEA6F4E73C7BBBCC11395B65E50"}}}]}`)
	if ok, _ := ReviewedAt(dir, headA); !ok {
		t.Fatal("commit ids compare case-insensitively")
	}
}
