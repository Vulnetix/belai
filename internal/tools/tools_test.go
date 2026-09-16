package tools

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinitionSchema(t *testing.T) {
	d := Definition{
		Name:        "Test",
		Description: "A test tool",
		Properties: map[string]Property{
			"path": {Type: "string", Description: "A path"},
		},
		Required: []string{"path"},
	}
	schema := d.Schema()
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) != 1 {
		t.Fatalf("properties len = %d", len(props))
	}
}

func TestDefinitionOpenAITool(t *testing.T) {
	d := Definition{Name: "Read", Description: "Read a file"}
	o := d.OpenAITool()
	if o.Type != "function" || o.Function.Name != "Read" {
		t.Fatalf("OpenAITool = %+v", o)
	}
}

func TestDefinitionAnthropicTool(t *testing.T) {
	d := Definition{Name: "Read", Description: "Read a file"}
	o := d.AnthropicTool()
	if o.Name != "Read" {
		t.Fatalf("AnthropicTool.Name = %q", o.Name)
	}
}

func TestGlobMatchBasic(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "readme.md", false},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "a/b/c.md", false},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "main.go", false},
		{"", "", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Fatalf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestGlobMatchDoubleStarSpansZero(t *testing.T) {
	if !matchGlob("**/*.go", "main.go") {
		t.Fatal("** should span zero segments")
	}
}

func TestGlobResult(t *testing.T) {
	r := GlobResult("a\nb", "fd")
	if r.Kind != KindGlob || r.Content != "a\nb" {
		t.Fatalf("GlobResult = %+v", r)
	}
	if r.Meta["backend"] != "fd" {
		t.Fatalf("backend = %v", r.Meta["backend"])
	}
}

func TestGrepResult(t *testing.T) {
	r := GrepResult("match", "rg")
	if r.Kind != KindGrep || r.Meta["backend"] != "rg" {
		t.Fatalf("GrepResult = %+v", r)
	}
}

func TestWebSearchResult(t *testing.T) {
	r := WebSearchResult("results")
	if r.Kind != KindWebSearch || r.Content != "results" {
		t.Fatalf("WebSearchResult = %+v", r)
	}
}

func TestWebFetchResult(t *testing.T) {
	r := WebFetchResult("html")
	if r.Kind != KindWebFetch || r.Content != "html" {
		t.Fatalf("WebFetchResult = %+v", r)
	}
}

func TestReadResult(t *testing.T) {
	r := ReadResult("hello")
	if r.Kind != KindRead || r.Content != "hello" {
		t.Fatalf("ReadResult = %+v", r)
	}
}

func TestRegistryFindNotFound(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Find("Missing"); ok {
		t.Fatal("expected not found")
	}
	if len(r.Names()) != 0 {
		t.Fatalf("len = %d", len(r.Names()))
	}
}

func TestGrepNormaliseLines(t *testing.T) {
	out := normaliseGrepLines([]byte("b:2:x\na:1:y\nc:10:z"), 100, 10)
	if len(out) != 3 || out[0] != "a:1:y" || out[1] != "b:2:x" || out[2] != "c:10:z" {
		t.Fatalf("lines = %v", out)
	}
}

func TestGrepNormaliseTruncatesLine(t *testing.T) {
	out := normaliseGrepLines([]byte("a:1:xxxxxxxxxx"), 5, 10)
	if len(out) != 1 || out[0] != "a:1:x…" {
		t.Fatalf("line = %q", out[0])
	}
}

func TestGrepNormaliseBoundsMatches(t *testing.T) {
	out := normaliseGrepLines([]byte("a:1:x\na:2:x\na:3:x"), 100, 2)
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
}

func TestGrepNormaliseSkipsNUL(t *testing.T) {
	out := normaliseGrepLines([]byte("a:1:x\x00y"), 100, 10)
	if len(out) != 0 {
		t.Fatalf("len = %d", len(out))
	}
}

func TestGrepKeyNoColon(t *testing.T) {
	if grepKey("foo") != "foo" {
		t.Fatalf("grepKey = %q", grepKey("foo"))
	}
}

func TestGrepKeyOneColon(t *testing.T) {
	if grepKey("foo:bar") != "foo:bar" {
		t.Fatalf("grepKey = %q", grepKey("foo:bar"))
	}
}

func TestGrepKeyNumeric(t *testing.T) {
	if grepKey("a:2:x") != "a:00000002" {
		t.Fatalf("grepKey = %q", grepKey("a:2:x"))
	}
}

func TestDefaultRegistry(t *testing.T) {
	r := Default(t.TempDir(), false)
	for _, name := range []string{"Read", "Bash", "Grep", "Glob", "WebFetch"} {
		if _, ok := r.Find(name); !ok {
			t.Fatalf("%s not in default registry", name)
		}
	}
}

func TestDefaultRegistryBashReadOnly(t *testing.T) {
	dir := t.TempDir()
	full := Default(dir, false)
	ro := Default(dir, true)
	fullBash, _ := full.Find("Bash")
	roBash, _ := ro.Find("Bash")
	if fullBash.(*Bash).ReadOnly {
		t.Fatal("full-mode Bash should not be readonly")
	}
	if !roBash.(*Bash).ReadOnly {
		t.Fatal("ro-mode Bash should be readonly")
	}
}

func TestGrepSubject(t *testing.T) {
	g := &Grep{}
	if g.Subject(map[string]any{"pattern": "x"}) != "x" {
		t.Fatal("expected x")
	}
	if g.Subject(map[string]any{}) != "" {
		t.Fatal("expected empty")
	}
}

func TestGlobSubject(t *testing.T) {
	g := &Glob{}
	if g.Subject(map[string]any{"pattern": "*.go"}) != "*.go" {
		t.Fatal("expected *.go")
	}
}

func TestGlobExecuteMissingPattern(t *testing.T) {
	g := &Glob{Root: t.TempDir()}
	_, err := g.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGlobExecuteWithPath(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("hello"), 0o600)
	g := &Glob{Root: root, MaxResults: 10}
	res, err := g.Execute(context.Background(), map[string]any{"pattern": "*.txt", "path": "sub"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "file.txt") {
		t.Fatalf("expected file.txt in %q", res.Content)
	}
}

func TestGlobExecuteBounded(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(root, "f"+string(rune('0'+i))+".txt")
		_ = os.WriteFile(name, []byte("x"), 0o600)
	}
	g := &Glob{Root: root, MaxResults: 3}
	res, err := g.Execute(context.Background(), map[string]any{"pattern": "*.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Count(res.Content, "\n")+1 != 3 {
		t.Fatalf("expected 3 results, got %q", res.Content)
	}
}

func TestWebFetchDefinition(t *testing.T) {
	wf := &WebFetch{}
	d := wf.Definition()
	if d.Name != "WebFetch" {
		t.Fatalf("name = %q", d.Name)
	}
}

func TestWebSearchDefinition(t *testing.T) {
	ws := &WebSearch{}
	d := ws.Definition()
	if d.Name != "WebSearch" {
		t.Fatalf("name = %q", d.Name)
	}
}

func TestWebFetchSubject(t *testing.T) {
	wf := &WebFetch{}
	if wf.Subject(map[string]any{"url": "https://example.com"}) != "https://example.com" {
		t.Fatal("subject mismatch")
	}
}

// TestWebFetchDefaultClientNoPanic pins the crash fixed in the redirect-guard
// setup: the default registry registers &WebFetch{} with a nil Client, and a
// fetch that reaches the redirect guard must derive its guarded client from
// the resolved base (http.DefaultClient), never by dereferencing the nil
// Client. A public literal IP passes the SSRF guard without a DNS round-trip,
// and the pre-cancelled context makes client.Do fail before any dial, so the
// test needs no network. Before the fix this panicked.
func TestWebFetchDefaultClientNoPanic(t *testing.T) {
	wf := &WebFetch{} // nil Client, exactly what tools.Default registers
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := wf.Execute(ctx, map[string]any{"url": "http://192.0.2.1/"})
	if err == nil {
		t.Fatal("expected a pre-dial error from the cancelled context, got nil")
	}
}

func TestWebSearchSubject(t *testing.T) {
	ws := &WebSearch{}
	if ws.Subject(map[string]any{"query": "q"}) != "q" {
		t.Fatal("subject mismatch")
	}
}

func TestBashSubjectEmpty(t *testing.T) {
	b := &Bash{}
	if b.Subject(map[string]any{}) != "" {
		t.Fatal("expected empty subject")
	}
}

func TestReadSubjectEmpty(t *testing.T) {
	r := &Read{}
	if r.Subject(map[string]any{}) != "" {
		t.Fatal("expected empty subject")
	}
}

func TestSchemaRequired(t *testing.T) {
	d := Definition{Required: []string{"a", "b"}}
	schema := d.Schema()
	req, _ := schema["required"].([]string)
	if len(req) != 2 {
		t.Fatalf("required = %v", req)
	}
}

func TestGlobWalkMatchesRelativeToSubpath(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("hello"), 0o600)
	g := &Glob{Root: root}
	got := g.walk("*.txt", "sub")
	if len(got) != 1 || got[0] != "sub/file.txt" {
		t.Fatalf("walk = %q, want [sub/file.txt]", got)
	}

	// A pattern without a leading directory still matches inside sub only,
	// never files outside it.
	got = g.walk("**/*.txt", "sub")
	if len(got) != 1 || got[0] != "sub/file.txt" {
		t.Fatalf("walk recursive = %q, want [sub/file.txt]", got)
	}
}

// TestWebFetchRejectsLoopbackViaDial pins the SSRF guard on the default
// (dedicated-transport) path: a loopback address is rejected at dial time by
// the validating DialContext before any connection is attempted.
func TestWebFetchRejectsLoopbackViaDial(t *testing.T) {
	wf := &WebFetch{}
	_, err := wf.Execute(context.Background(), map[string]any{"url": "http://127.0.0.1/"})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected loopback rejection, got %v", err)
	}
}

func TestForbiddenIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"192.168.1.1", true},
		{"169.254.1.1", true},
		{"0.0.0.0", true},
		{"::1", true},
		{"8.8.8.8", false},
		{"93.184.216.34", false},
	}
	for _, c := range cases {
		if got := forbiddenIP(net.ParseIP(c.ip)); got != c.want {
			t.Fatalf("forbiddenIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}
