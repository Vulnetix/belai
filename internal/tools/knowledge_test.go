package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeKnowledge struct {
	hits     []KnowledgeHit
	docs     []string
	searches atomic.Int32
	lastQ    atomic.Value
}

func (f *fakeKnowledge) Search(q string) []KnowledgeHit {
	f.searches.Add(1)
	f.lastQ.Store(q)
	return f.hits
}

func (f *fakeKnowledge) Match(m func(string) bool) []string {
	var out []string
	for _, d := range f.docs {
		if m(d) {
			out = append(out, d)
		}
	}
	return out
}

func knowledgeFixture(t *testing.T) (*Registry, string, *fakeKnowledge) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc authenticate() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := Default(root, false)
	fk := &fakeKnowledge{
		docs: []string{"kb+project/.vulnetix/sast.sarif", "kb+prof/handbook/auth.md"},
		hits: []KnowledgeHit{
			{Address: "kb+prof/handbook/auth.md", Source: "/docs/auth.md", Start: 4, End: 5, Text: "Rotate the signing key every ninety days.\nRevoke tokens on logout.", Score: 0.62},
			{Address: "kb+project/.vulnetix/sast.sarif", Source: "/p/.vulnetix/sast.sarif", Start: 7, End: 7, Text: "SARIF result rule VNX-GO-SQLI severity high in db/query.go line 40", Score: 0.31},
			{Address: "kb+prof/handbook/ops.md", Source: "/docs/ops.md", Start: 1, End: 1, Text: "weak", Score: 0.12},
		},
	}
	reg.KnowledgeHub().Set(fk)
	return reg, root, fk
}

func run(t *testing.T, reg *Registry, ctx context.Context, name string, args map[string]any) Result {
	t.Helper()
	tool, ok := reg.Find(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	res, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func TestHubIsSharedByEveryNarrowing(t *testing.T) {
	reg, _, fk := knowledgeFixture(t)
	for name, r := range map[string]*Registry{
		"plan": reg.Plan(), "readonly": reg.ReadOnly(), "only": reg.Only("Grep", "Read"), "with": reg.With(UpdatePlan{}),
		"surface": reg.ReadOnlySurface(), "noplan": reg.WithoutPlanOnly(),
	} {
		if r.KnowledgeHub().Get() == nil || r.KnowledgeHub().Get() != Knowledge(fk) {
			t.Errorf("%s registry lost the knowledge hub", name)
		}
	}
	var nilReg *Registry
	if nilReg.KnowledgeHub() != nil || nilReg.KnowledgeHub().Get() != nil {
		t.Fatal("a nil registry has no hub")
	}
}

func TestGrepAddsKnowledgeRowsOnlyForAModelCall(t *testing.T) {
	reg, _, fk := knowledgeFixture(t)
	args := map[string]any{"pattern": "authenticate|signing key", "output_mode": "content"}

	plain := run(t, reg, context.Background(), "Grep", args)
	if strings.Contains(plain.Content, "kb+") || fk.searches.Load() != 0 {
		t.Fatalf("a harness call must get the filesystem alone: %q", plain.Content)
	}

	res := run(t, reg, WithKnowledge(context.Background()), "Grep", args)
	if res.Kind != KindGrep {
		t.Fatalf("kind = %v: knowledge rows must not change the Grep kind", res.Kind)
	}
	if !strings.Contains(res.Content, "main.go:3:func authenticate() {}") {
		t.Fatalf("the filesystem rows are still there: %q", res.Content)
	}
	for _, want := range []string{
		KnowledgeRowsHeader,
		"kb+prof/handbook/auth.md:4:Rotate the signing key every ninety days.",
		"kb+prof/handbook/auth.md:5:Revoke tokens on logout.",
		"kb+project/.vulnetix/sast.sarif:7:SARIF result rule VNX-GO-SQLI",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing %q in\n%s", want, res.Content)
		}
	}
	if q, _ := fk.lastQ.Load().(string); q != "authenticate signing key" {
		t.Fatalf("query = %q", q)
	}
	if strings.Index(res.Content, "main.go") > strings.Index(res.Content, KnowledgeRowsHeader) {
		t.Fatal("filesystem rows come first")
	}
}

func TestGrepKnowledgeIsSkippedWhenScopedOrCounted(t *testing.T) {
	reg, root, fk := knowledgeFixture(t)
	ctx := WithKnowledge(context.Background())
	for name, args := range map[string]map[string]any{
		"path":     {"pattern": "authenticate", "path": root},
		"glob":     {"pattern": "authenticate", "glob": "*.go"},
		"type":     {"pattern": "authenticate", "type": "go"},
		"count":    {"pattern": "authenticate", "output_mode": "count"},
		"no words": {"pattern": ".*"},
	} {
		res := run(t, reg, ctx, "Grep", args)
		if strings.Contains(res.Content, "kb+") {
			t.Errorf("%s: knowledge must not ride on a scoped search: %q", name, res.Content)
		}
	}
	if fk.searches.Load() != 0 {
		t.Fatalf("a scoped search must not even ask the store: %d searches", fk.searches.Load())
	}
}

func TestGrepFilesModeListsDocumentAddresses(t *testing.T) {
	reg, _, _ := knowledgeFixture(t)
	res := run(t, reg, WithKnowledge(context.Background()), "Grep", map[string]any{"pattern": "authenticate signing", "output_mode": "files_with_matches"})
	if !strings.Contains(res.Content, KnowledgeFilesHeader) || !strings.Contains(res.Content, "\nkb+prof/handbook/auth.md") || strings.Contains(res.Content, "Rotate the signing key") {
		t.Fatalf("files mode lists addresses only:\n%s", res.Content)
	}
}

func TestGrepWithoutAStoreIsUnchanged(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("hello world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := Default(root, false)
	res := run(t, reg, WithKnowledge(context.Background()), "Grep", map[string]any{"pattern": "hello"})
	if res.Content != "a.go:1:hello world" {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestGlobListsMatchingAndSimilarDocuments(t *testing.T) {
	reg, _, _ := knowledgeFixture(t)
	ctx := WithKnowledge(context.Background())

	res := run(t, reg, ctx, "Glob", map[string]any{"pattern": "**/*.sarif"})
	if res.Kind != KindGlob || !strings.Contains(res.Content, "kb+project/.vulnetix/sast.sarif") || strings.Contains(res.Content, "(similar)\n") && false {
		t.Fatalf("glob = %q", res.Content)
	}
	if !strings.Contains(res.Content, KnowledgeFilesHeader) {
		t.Fatalf("missing header: %q", res.Content)
	}

	res = run(t, reg, ctx, "Glob", map[string]any{"pattern": "**/*signing*"})
	if !strings.Contains(res.Content, "kb+prof/handbook/auth.md  (similar)") {
		t.Fatalf("a document found by meaning is marked similar: %q", res.Content)
	}
	if strings.Contains(res.Content, "main.go") {
		t.Fatalf("filesystem match leaked: %q", res.Content)
	}

	scoped := run(t, reg, ctx, "Glob", map[string]any{"pattern": "**/*.sarif", "path": "."})
	if strings.Contains(scoped.Content, "kb+") {
		t.Fatalf("a path-scoped Glob is filesystem only: %q", scoped.Content)
	}
	harness := run(t, reg, context.Background(), "Glob", map[string]any{"pattern": "**/*.sarif"})
	if strings.Contains(harness.Content, "kb+") {
		t.Fatalf("a harness Glob is filesystem only: %q", harness.Content)
	}
}

func TestReadAppendsRelatedPassages(t *testing.T) {
	reg, root, _ := knowledgeFixture(t)
	args := map[string]any{"file_path": filepath.Join(root, "main.go")}
	res := run(t, reg, WithKnowledge(context.Background()), "Read", args)
	if res.Kind != KindRead {
		t.Fatalf("kind = %v: Read must stay classified", res.Kind)
	}
	if !strings.Contains(res.Content, "func authenticate()") || !strings.Contains(res.Content, ReadKnowledgeHeader) {
		t.Fatalf("content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "kb+prof/handbook/auth.md:4-5 (62%)") || !strings.Contains(res.Content, "| Rotate the signing key every ninety days.") {
		t.Fatalf("related block wrong:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "weak") {
		t.Fatalf("a passage under the similarity floor must not be appended:\n%s", res.Content)
	}
	if res.Meta["abs"] == nil {
		t.Fatal("Read meta must keep abs for the withheld-file record")
	}

	plain := run(t, reg, context.Background(), "Read", args)
	if strings.Contains(plain.Content, "kb+") || strings.Contains(plain.Content, ReadKnowledgeHeader) {
		t.Fatalf("a harness Read (attachment, prefetch) must not carry passages: %q", plain.Content)
	}
}

func TestReadSkipsItsOwnSourceAndVerbatim(t *testing.T) {
	reg, root, fk := knowledgeFixture(t)
	self := filepath.Join(root, "main.go")
	fk.hits = []KnowledgeHit{{Address: "kb+session/main.go", Source: self, Start: 1, End: 3, Text: "package main", Score: 0.9}}
	res := run(t, reg, WithKnowledge(context.Background()), "Read", map[string]any{"file_path": self})
	if strings.Contains(res.Content, "kb+") {
		t.Fatalf("a file's own indexed copy is not related knowledge: %q", res.Content)
	}
	fk.hits = []KnowledgeHit{{Address: "kb+prof/a.md", Source: "/x", Start: 1, End: 1, Text: "text", Score: 0.9}}
	v := &Read{Root: root, MaxBytes: 1 << 16, Verbatim: true, Knowledge: reg.KnowledgeHub(), Cwd: reg.Cwd()}
	got, err := v.Execute(WithKnowledge(context.Background()), map[string]any{"file_path": "main.go"})
	if err != nil || strings.Contains(got.Content, "kb+") {
		t.Fatalf("verbatim must be the file's bytes only: %q err=%v", got.Content, err)
	}
}

func TestKnowledgeQueryStripsRegexSyntax(t *testing.T) {
	for in, want := range map[string]string{
		`func\s+Auth\w*\(`: "func Auth",
		`foo|bar`:          "foo bar",
		`.*`:               "",
		`signing key`:      "signing key",
	} {
		if got := knowledgeQuery(in); got != want {
			t.Errorf("knowledgeQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKnowledgeRowsClipAndNumber(t *testing.T) {
	long := strings.Repeat("x", 500)
	out := knowledgeRows([]KnowledgeHit{{Address: "kb+p/a.md", Start: 10, End: 12, Text: "one\n\n" + long}})
	lines := strings.Split(out, "\n")
	if lines[1] != "kb+p/a.md:10:one" || !strings.HasPrefix(lines[2], "kb+p/a.md:12:xxx") || !strings.HasSuffix(lines[2], "…") {
		t.Fatalf("rows = %q", lines)
	}
}
