package vex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/scanartifacts"
)

var fixedNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func base(v kanban.Verdict) Input {
	return Input{Finding: "GHSA-aaaa-bbbb-cccc", Verdict: v, Package: "lodash", Ecosystem: "npm", Version: "4.17.0",
		Commit: "1111111111111111111111111111111111111111", Now: fixedNow}
}

func decode(t *testing.T, data []byte) document {
	t.Helper()
	var d document
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestComposeMapsEachVerdict(t *testing.T) {
	fp := base(kanban.VerdictFalsePositive)
	fp.Justification, fp.Impact = NotInExecutePath, "the function is never imported"
	noFix := base(kanban.VerdictNoFix)
	noFix.Action = "wait for upstream 4.18"
	for name, c := range map[string]struct {
		in     Input
		status string
	}{
		"fixed":       {base(kanban.VerdictFixed), "fixed"},
		"false pos":   {fp, "not_affected"},
		"no fix":      {noFix, "affected"},
		"needs human": {base(kanban.VerdictNeedsHuman), "under_investigation"},
	} {
		data, err := Compose(c.in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d := decode(t, data)
		if d.Context != Context || len(d.Statements) != 1 || d.Statements[0].Status != c.status {
			t.Fatalf("%s: %+v", name, d)
		}
		if d.Statements[0].Products[0].ID != "pkg:npm/lodash@4.17.0" || d.Statements[0].Vulnerability.Name != "GHSA-aaaa-bbbb-cccc" {
			t.Fatalf("%s: %+v", name, d.Statements[0])
		}
		if d.Timestamp != "2026-09-30T12:00:00Z" || d.Version != 1 || !strings.HasPrefix(d.ID, "urn:belai:vex:GHSA-aaaa-bbbb-cccc:") {
			t.Fatalf("%s: header %+v", name, d)
		}
	}
}

func TestComposeRefusesWhatOpenVEXRequires(t *testing.T) {
	if _, err := Compose(base(kanban.VerdictFalsePositive)); err == nil {
		t.Fatal("a false positive without a justification was accepted")
	}
	bad := base(kanban.VerdictFalsePositive)
	bad.Justification = "because I said so"
	if _, err := Compose(bad); err == nil {
		t.Fatal("a made-up justification was accepted")
	}
	if _, err := Compose(base(kanban.VerdictNoFix)); err == nil {
		t.Fatal("an affected statement without an action was accepted")
	}
	if _, err := Compose(base(kanban.VerdictRejected)); err != ErrNoDocument {
		t.Fatalf("rejected: %v", err)
	}
	bad = base(kanban.VerdictFixed)
	bad.Finding = "../../etc/passwd"
	if _, err := Compose(bad); err == nil {
		t.Fatal("a path was accepted as a finding id")
	}
}

func TestComposeCleansModelText(t *testing.T) {
	in := base(kanban.VerdictNoFix)
	in.Action = "line one\n\x1b[31mred\x1b[0m ‮evil" + strings.Repeat("x", 900)
	data, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	a := decode(t, data).Statements[0].ActionStatement
	if strings.ContainsAny(a, "\n\x1b‮") || len([]rune(a)) > maxStatement {
		t.Fatalf("action not cleaned: %q", a)
	}
}

func TestPurl(t *testing.T) {
	for _, c := range []struct{ eco, name, ver, want string }{
		{"npm", "@scope/pkg", "1.0.0", "pkg:npm/%40scope/pkg@1.0.0"},
		{"golang", "golang.org/x/sys", "0.38.0", "pkg:golang/golang.org/x/sys@0.38.0"},
		{"weird", "thing", "", "pkg:generic/thing"},
	} {
		if got := Purl(c.eco, c.name, c.ver); got != c.want {
			t.Errorf("Purl(%s,%s,%s) = %s, want %s", c.eco, c.name, c.ver, got, c.want)
		}
	}
	in := base(kanban.VerdictFixed)
	in.Package, in.Repo = "", "belai"
	if p := in.product(); p != "pkg:generic/belai@1111111111111111111111111111111111111111" {
		t.Fatalf("repository product = %s", p)
	}
}

func TestWriteIsConfinedAtomicAndVersioned(t *testing.T) {
	root := t.TempDir()
	rel, err := Write(root, base(kanban.VerdictFixed))
	if err != nil || rel != ".vulnetix/vex/GHSA-aaaa-bbbb-cccc.openvex.json" {
		t.Fatalf("write: %q %v", rel, err)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil || decode(t, data).Version != 1 {
		t.Fatalf("first document: %v", err)
	}
	if _, err := Write(root, base(kanban.VerdictNeedsHuman)); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if d := decode(t, data); d.Version != 2 || d.Statements[0].Status != "under_investigation" {
		t.Fatalf("second document: %+v", d)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Fatal("a .part file was left behind")
	}
	// The file parses with the reader the scan views use.
	_, _, _, keys, err := scanartifacts.ParseOpenVEX(context.Background(), strings.NewReader(string(data)), scanartifacts.DefaultMaxBytes)
	if err != nil {
		t.Fatalf("ParseOpenVEX: %v", err)
	}
	_ = keys
}

func TestWriteRefusesSymlinksAndBadNames(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".vulnetix")); err != nil {
		t.Skip("no symlinks here")
	}
	if _, err := Write(root, base(kanban.VerdictFixed)); err == nil {
		t.Fatal("wrote through a symlinked .vulnetix")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("something was written outside the root: %v", entries)
	}
	in := base(kanban.VerdictFixed)
	in.Finding = ".hidden"
	if _, err := Write(t.TempDir(), in); err == nil {
		t.Fatal("a dot-file finding id was accepted")
	}
	if FileName("sast:go_sql:abcd1234") != "sast_go_sql_abcd1234.openvex.json" {
		t.Fatal("colons must not reach a file name")
	}
}
