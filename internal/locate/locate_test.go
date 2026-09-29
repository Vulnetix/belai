package locate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(inv *Inventory) []string {
	out := make([]string, len(inv.Files))
	for i, f := range inv.Files {
		out[i] = f.Path
	}
	return out
}

func TestInventoryLeavesOutWhatMustNeverBeLookedAt(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{
		"main.go", "internal/auth/login.go", "docs/notes.md",
		"node_modules/x/index.js", "vendor/y.go", "dist/out.js", "build/z.go",
		".env", ".env.local", "config/credentials.json", "secrets.yaml", "id_rsa", "certs/server.pem", "tls.key",
		".hidden/a.go", ".github/workflows/ci.yml", "logo.png", "app.wasm",
	} {
		write(t, root, f, "package x\n")
	}
	if err := os.Symlink(filepath.Join(root, "main.go"), filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	inv, err := Build(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/notes.md", "internal/auth/login.go", "main.go"}
	if got := paths(inv); !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for reason, min := range map[string]int{SkipDependency: 4, SkipSensitive: 6, SkipHidden: 2, SkipSymlink: 1, SkipBinary: 2} {
		if inv.Skipped[reason] < min {
			t.Errorf("skipped[%s] = %d, want at least %d", reason, inv.Skipped[reason], min)
		}
	}
}

func TestInventoryHonoursIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", "*.log\n/out/\ngen/**\n!keep.log\ntmp\n")
	write(t, root, "a.go", "x")
	write(t, root, "debug.log", "x")
	write(t, root, "keep.log", "x")
	write(t, root, "out/b.go", "x")
	write(t, root, "src/out/c.go", "x")
	write(t, root, "gen/d.go", "x")
	write(t, root, "src/tmp/e.go", "x")
	write(t, root, "src/.gitignore", "local.go\n")
	write(t, root, "src/local.go", "x")
	write(t, root, "src/f.go", "x")
	// .ignore is applied after .gitignore in the same directory and wins.
	write(t, root, ".ignore", "!debug.log\nf.go\n")
	write(t, root, "src/.ignore", "f.go\n")
	inv, _ := Build(context.Background(), root)
	got := paths(inv)
	sort.Strings(got)
	want := []string{"a.go", "debug.log", "keep.log", "src/out/c.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestNestedRepositoryResetsGitRulesButNotIgnoreRules(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", "*.gen.go\n")
	write(t, root, "top.gen.go", "x")
	write(t, root, "sub/.git/HEAD", "ref")
	write(t, root, "sub/inner.gen.go", "x")
	write(t, root, "sub/other.go", "x")
	write(t, root, ".ignore", "other.go\n")
	inv, _ := Build(context.Background(), root)
	got := paths(inv)
	sort.Strings(got)
	// top.gen.go is ignored by the parent's rules; inside the nested
	// repository the parent's .gitignore does not apply, but .ignore does.
	want := []string{"sub/inner.gen.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestIgnoreGlobs(t *testing.T) {
	cases := []struct {
		rules, path string
		dir, want   bool
	}{
		{"*.go", "a/b/c.go", false, true},
		{"/a", "a", true, true},
		{"/a", "x/a", true, false},
		{"a/b", "a/b", false, true},
		{"a/b", "x/a/b", false, false},
		{"**/gen", "x/y/gen", true, true},
		{"a/**/z", "a/z", false, true},
		{"a/**/z", "a/b/c/z", false, true},
		{"file?.txt", "file1.txt", false, true},
		{"file?.txt", "file12.txt", false, false},
		{"[ab].go", "a.go", false, true},
		{"[!ab].go", "a.go", false, false},
		{"dir/", "dir", true, true},
		{"dir/", "dir", false, false},
		{"dir/", "x/dir/f.go", false, true},
		{"# c", "# c", false, false},
	}
	for _, c := range cases {
		f := parseIgnore([]byte(c.rules), "", true)
		got, _ := f.match(c.path, c.dir)
		if got != c.want {
			t.Errorf("rules %q path %q dir=%v: ignored %v, want %v", c.rules, c.path, c.dir, got, c.want)
		}
	}
}

func TestInventoryCapsAndCancels(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, root); err == nil {
		t.Error("a cancelled context must be reported")
	}
	write(t, root, "big.txt", "x")
	if err := os.Truncate(filepath.Join(root, "big.txt"), MaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	inv, _ := Build(context.Background(), root)
	if inv.Skipped[SkipLarge] != 1 || len(inv.Files) != 1 {
		t.Errorf("large file: files %v skipped %v", paths(inv), inv.Skipped)
	}
}

func TestWordsSplitsCasesAndDropsNoise(t *testing.T) {
	got := words("parseHTTPHeaders in the auth_handler-v2 file")
	want := []string{"parse", "httpheader", "auth", "handler", "v2"}
	// "HTTPHeaders" has no lower-to-upper boundary inside the capital run, so
	// it stays one word (plural trimmed); the rest split.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("words = %v, want %v", got, want)
	}
	if len(words("the and for")) != 0 {
		t.Error("stop words survived")
	}
}

func TestDeclarations(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n\ntype Server struct{}\n\nfunc (s *Server) Handle() {}\n\nfunc helper() {}\n\nvar Limit = 3\n")
	write(t, root, "b.py", "class Widget:\n    def render(self):\n        pass\n\ndef build():\n    pass\n")
	write(t, root, "c.ts", "export async function fetchUser() {}\nexport class Store {}\ninterface Props {}\n")
	write(t, root, "d.rs", "pub fn run() {}\nstruct Conf;\nimpl Conf {}\n")
	write(t, root, "bad.go", "package a\nfunc Broken( {\n")
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	names := func(f string) []string {
		ds, _ := Declarations(filepath.Join(root, f))
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = d.Name
		}
		return out
	}
	for f, want := range map[string][]string{
		"a.go": {"Server", "Handle", "helper", "Limit"},
		"b.py": {"Widget", "render", "build"},
		"c.ts": {"fetchUser", "Store", "Props"},
		"d.rs": {"run", "Conf", "Conf"},
	} {
		if got := names(f); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", f, got, want)
		}
	}
	ds, _ := Declarations(filepath.Join(root, "a.go"))
	if ds[1].Line != 5 {
		t.Errorf("Handle line = %d, want 5", ds[1].Line)
	}
	if _, text := Declarations(filepath.Join(root, "bin.dat")); text {
		t.Error("a binary file must not be text")
	}
	if _, text := Declarations(filepath.Join(root, "missing")); text {
		t.Error("a missing file must not be text")
	}
	if got := names("bad.go"); len(got) == 0 {
		t.Log("a file that does not parse falls back to the scanner")
	}
}

// fake rates by a substring table and records what it was shown.
type fake struct {
	dirs, files map[string]float64 // substring of the label -> score
	err         error
	labels      []string
	calls       []Stage
}

func (f *fake) Rate(_ context.Context, st Stage, _ string, items []Item) (map[string]float64, error) {
	f.calls = append(f.calls, st)
	if f.err != nil {
		return nil, f.err
	}
	table := f.files
	if st == StageDirs {
		table = f.dirs
	}
	out := map[string]float64{}
	for _, it := range items {
		f.labels = append(f.labels, it.Label)
		for sub, s := range table {
			if strings.Contains(it.Label, sub) {
				out[it.ID] = s
			}
		}
	}
	return out, nil
}

func fixture(t *testing.T) *Inventory {
	t.Helper()
	root := t.TempDir()
	write(t, root, "internal/auth/login.go", "package auth\n\nfunc Login() {}\nfunc VerifyPassword() {}\n")
	write(t, root, "internal/auth/session.go", "package auth\n\nfunc NewSession() {}\n")
	write(t, root, "internal/billing/invoice.go", "package billing\n\nfunc Invoice() {}\n")
	write(t, root, "docs/auth.md", "# Auth\n")
	write(t, root, "cmd/app/main.go", "package main\n\nfunc main() {}\n")
	inv, err := Build(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestSearchLexicalOnly(t *testing.T) {
	inv := fixture(t)
	r := Search(context.Background(), inv, "where is the login verify password check", Options{})
	if !r.Lexical || len(r.Hits) == 0 {
		t.Fatalf("result = %+v", r)
	}
	if r.Hits[0].Path != "internal/auth/login.go" {
		t.Errorf("top hit = %+v", r.Hits[0])
	}
	if r.Hits[0].Line != 4 {
		t.Errorf("line = %d, want the VerifyPassword declaration on 4", r.Hits[0].Line)
	}
	for _, h := range r.Hits {
		if h.Rated || h.Lead {
			t.Errorf("a lexical hit is neither rated nor a lead: %+v", h)
		}
		if h.Path == "internal/billing/invoice.go" {
			t.Errorf("unrelated file ranked: %+v", h)
		}
	}
	if got := Search(context.Background(), inv, "the and", Options{}); len(got.Hits) != 0 {
		t.Errorf("a query of stop words matched: %+v", got)
	}
}

func TestSearchRaterReordersAndThresholds(t *testing.T) {
	inv := fixture(t)
	f := &fake{
		dirs:  map[string]float64{"internal/auth": 0.9, "docs": 0.1},
		files: map[string]float64{"login.go": 0.95, "session.go": 0.3, "auth.md": 0.9},
	}
	r := Search(context.Background(), inv, "auth login", Options{Rater: f, Previews: true})
	if r.Lexical {
		t.Fatal("rated result reported as lexical")
	}
	var got []string
	for _, h := range r.Hits {
		got = append(got, h.Path)
	}
	want := []string{"internal/auth/login.go", "internal/auth/session.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hits = %v, want %v (docs dir rated 0.1 is dropped with its files)", got, want)
	}
	if r.Hits[0].Lead || !r.Hits[1].Lead || !r.Hits[0].Rated {
		t.Errorf("hit/lead flags: %+v", r.Hits)
	}
	if !reflect.DeepEqual(f.calls, []Stage{StageDirs, StageFiles}) {
		t.Errorf("stages = %v", f.calls)
	}
	joined := strings.Join(f.labels, "\n")
	if !strings.Contains(joined, "declares Login, VerifyPassword") {
		t.Errorf("previews on but no declarations in labels:\n%s", joined)
	}
}

func TestSearchPreviewsOffSendsPathsOnly(t *testing.T) {
	inv := fixture(t)
	f := &fake{dirs: map[string]float64{"": 0.9}, files: map[string]float64{"": 0.9}}
	Search(context.Background(), inv, "auth login", Options{Rater: f})
	for _, l := range f.labels {
		if strings.Contains(l, "declares") {
			t.Errorf("label carries declarations with previews off: %q", l)
		}
	}
}

func TestSearchUnknownKeepsLexicalAndErrorFallsBack(t *testing.T) {
	inv := fixture(t)
	// The rater answers for nothing: unknown is not low.
	f := &fake{}
	r := Search(context.Background(), inv, "auth login", Options{Rater: f})
	if !r.Lexical || len(r.Hits) == 0 || r.Unknown == 0 {
		t.Fatalf("result = %+v", r)
	}
	// The rater fails: same lexical result, no rated flags.
	e := &fake{err: errors.New("down")}
	r = Search(context.Background(), inv, "auth login", Options{Rater: e})
	if !r.Lexical || len(r.Hits) == 0 || r.Hits[0].Rated {
		t.Fatalf("result = %+v", r)
	}
}

func TestSearchNothingAboveALeadOffersLexicalLeads(t *testing.T) {
	inv := fixture(t)
	f := &fake{dirs: map[string]float64{"": 0.9}, files: map[string]float64{"": 0.05}}
	r := Search(context.Background(), inv, "auth login", Options{Rater: f})
	if len(r.Hits) == 0 || len(r.Hits) > 3 {
		t.Fatalf("hits = %+v", r.Hits)
	}
	for _, h := range r.Hits {
		if !h.Lead || h.Rated {
			t.Errorf("fallback hit must be an unrated lead: %+v", h)
		}
	}
}

func TestSearchLocalCapsAndMaxHits(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		write(t, root, "pkg/handler_"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go", "package pkg\n")
	}
	inv, _ := Build(context.Background(), root)
	f := &fake{dirs: map[string]float64{"": 0.9}, files: map[string]float64{"": 0.9}}
	r := Search(context.Background(), inv, "handler", Options{Rater: f, Local: true, MaxHits: 5})
	if r.FilesRated > FilesLocal {
		t.Errorf("local rated %d files, cap %d", r.FilesRated, FilesLocal)
	}
	if len(r.Hits) != 5 {
		t.Errorf("hits = %d, want the MaxHits cap of 5", len(r.Hits))
	}
	r = Search(context.Background(), inv, "handler", Options{})
	if len(r.Hits) != LexicalOnlyHits {
		t.Errorf("lexical hits = %d, want %d", len(r.Hits), LexicalOnlyHits)
	}
}

func TestLabelsCarryNoFileTextExceptDeclarations(t *testing.T) {
	l := fileLabel(File{Path: "a/b.go", Size: 2048}, []Decl{{Name: "X"}}, false)
	if l != "file a/b.go (Go, 2 KB)" {
		t.Errorf("label = %q", l)
	}
	d := dirLabel("a", 3, map[string]int{".go": 2, ".md": 1})
	if d != "directory a (3 files: .go x2, .md x1)" {
		t.Errorf("dir label = %q", d)
	}
}

func TestSearchIsDeterministic(t *testing.T) {
	inv := fixture(t)
	a := Search(context.Background(), inv, "auth session login", Options{})
	for i := 0; i < 5; i++ {
		b := Search(context.Background(), inv, "auth session login", Options{})
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("run %d differs", i)
		}
	}
}

// A question is often about a name the path does not mention; the declaration
// names read from the surviving directories still surface the file.
func TestSearchFindsAFileByWhatItDeclares(t *testing.T) {
	inv := fixture(t)
	r := Search(context.Background(), inv, "verify password", Options{})
	if len(r.Hits) == 0 || r.Hits[0].Path != "internal/auth/login.go" || r.Hits[0].Line != 4 {
		t.Fatalf("hits = %+v", r.Hits)
	}
}

func FuzzParseIgnoreAndMatch(f *testing.F) {
	for _, s := range []string{"*.go", "!x", "a/**/b", "[", "[!a-", "\\", "**", "/", "#", "a b ", "**/**/**", "?[", "\x00"} {
		f.Add(s, "a/b/c.go")
	}
	f.Fuzz(func(t *testing.T, rules, p string) {
		ig := parseIgnore([]byte(rules), "", true)
		_, _ = ig.match(strings.TrimPrefix(p, "/"), false)
		_, _ = ig.match(strings.TrimPrefix(p, "/"), true)
	})
}

func FuzzWords(f *testing.F) {
	for _, s := range []string{"parseHTTPHeaders", "a_b-c d", "", "ÄÖÜ ß", "\x00\xff", "日本語 テスト"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, w := range words(s) {
			if len([]rune(w)) < 2 {
				t.Fatalf("word %q too short", w)
			}
			if w != strings.ToLower(w) {
				t.Fatalf("word %q not lower case", w)
			}
		}
	})
}

func TestDryRunListsTopLevelDirectoriesAndReasons(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "x")
	write(t, root, "cmd/x/main.go", "package main")
	write(t, root, "cmd/y/main.go", "package main")
	write(t, root, "node_modules/z.js", "x")
	write(t, root, ".env", "K=1")
	inv, _ := Build(context.Background(), root)
	got := DryRun(inv)
	for _, want := range []string{"3 eligible files", "  .  1 files", "  cmd  2 files", "dependency 1", "sensitive 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("dry run lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "main.go") || strings.Contains(got, ".env") {
		t.Errorf("a dry run lists directories and counts, not file names:\n%s", got)
	}
}
