package tools

import (
	"context"
	"os"

	"github.com/vulnetix/signet/internal/repoindex"
	"path/filepath"
	"strings"
	"testing"
)

// cwdTree builds root/internal/tools plus a couple of files.
func cwdTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for path, body := range map[string]string{
		"top.txt":                  "top",
		"internal/mid.txt":         "mid",
		"internal/tools/leaf.txt":  "leaf",
		"internal/tools/other.txt": "other",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

func TestCwdStartsAtRoot(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if c.Rel() != "" {
		t.Errorf("Rel = %q, want empty", c.Rel())
	}
	if c.Dir() != root {
		t.Errorf("Dir = %q, want %q", c.Dir(), root)
	}
	if c.Root() != root {
		t.Errorf("Root = %q, want %q", c.Root(), root)
	}
}

// A relative move is relative to where we are, so moves compose.
func TestCwdChangeIsRelativeToCurrent(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if _, err := c.Change("internal"); err != nil {
		t.Fatalf("Change(internal): %v", err)
	}
	if c.Rel() != "internal" {
		t.Fatalf("Rel = %q, want internal", c.Rel())
	}
	if _, err := c.Change("tools"); err != nil {
		t.Fatalf("Change(tools): %v", err)
	}
	if c.Rel() != "internal/tools" {
		t.Fatalf("Rel = %q, want internal/tools", c.Rel())
	}
	if c.Dir() != filepath.Join(root, "internal", "tools") {
		t.Fatalf("Dir = %q", c.Dir())
	}
}

// A leading "/" means the session root, not the filesystem root: it is the
// one absolute spelling that exists here.
func TestCwdLeadingSlashIsRootRelative(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if _, err := c.Change("internal/tools"); err != nil {
		t.Fatalf("Change: %v", err)
	}
	if _, err := c.Change("/internal"); err != nil {
		t.Fatalf("Change(/internal): %v", err)
	}
	if c.Rel() != "internal" {
		t.Fatalf("Rel = %q, want internal", c.Rel())
	}
	if _, err := c.Change("/"); err != nil {
		t.Fatalf("Change(/): %v", err)
	}
	if c.Rel() != "" {
		t.Fatalf("Rel = %q, want the root", c.Rel())
	}
}

func TestCwdDotDotMovesUp(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if _, err := c.Change("internal/tools"); err != nil {
		t.Fatalf("Change: %v", err)
	}
	if _, err := c.Change(".."); err != nil {
		t.Fatalf("Change(..): %v", err)
	}
	if c.Rel() != "internal" {
		t.Fatalf("Rel = %q, want internal", c.Rel())
	}
}

// The root is a boundary, not a starting point: no sequence of moves escapes
// it, and a refused move leaves the working directory exactly where it was.
func TestCwdCannotEscapeRoot(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if _, err := c.Change("internal"); err != nil {
		t.Fatalf("Change: %v", err)
	}
	for _, bad := range []string{"../..", "/..", "../../etc", os.TempDir()} {
		if _, err := c.Change(bad); err == nil {
			t.Errorf("Change(%q) succeeded, want an error", bad)
		}
		if c.Rel() != "internal" {
			t.Fatalf("a refused move moved the working directory to %q", c.Rel())
		}
	}
}

// Moving onto a file, or onto something absent, is refused: a working
// directory that is not a directory would break every following relative path.
func TestCwdChangeRejectsNonDirectories(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if _, err := c.Change("top.txt"); err == nil {
		t.Error("Change onto a file succeeded")
	}
	if _, err := c.Change("nope"); err == nil {
		t.Error("Change onto a missing path succeeded")
	}
	if _, err := c.Change("   "); err == nil {
		t.Error("Change with a blank path succeeded")
	}
	if c.Rel() != "" {
		t.Fatalf("a refused move moved the working directory to %q", c.Rel())
	}
}

// A nil tracker behaves exactly as no tracker at all, which is what keeps a
// tool constructed without one working unchanged.
func TestNilCwdBehavesLikeNoTracker(t *testing.T) {
	var c *Cwd
	if c.Rel() != "" || c.Dir() != "" || c.Root() != "" {
		t.Fatal("nil tracker did not answer empty")
	}
	if _, err := c.Change("x"); err == nil {
		t.Fatal("Change on a nil tracker succeeded")
	}
	root := cwdTree(t)
	res, err := resolvePath(root, nil, "internal/mid.txt")
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}
	if res.Rel != filepath.Join("internal", "mid.txt") {
		t.Fatalf("rel = %q", res.Rel)
	}
	if got := baseDir(root, nil); got != root {
		t.Fatalf("baseDir = %q, want %q", got, root)
	}
}

// Read resolves its path through the working directory, and the result is
// still reported to the permission layer relative to the root.
func TestReadFollowsWorkingDirectory(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	r := &Read{Root: root, Cwd: c}

	if _, err := c.Change("internal/tools"); err != nil {
		t.Fatalf("Change: %v", err)
	}
	res, err := r.Execute(context.Background(), map[string]any{"path": "leaf.txt"})
	if err != nil {
		t.Fatalf("Read(leaf.txt) after Cd: %v", err)
	}
	if res.Content != "leaf" {
		t.Fatalf("content = %q, want leaf", res.Content)
	}

	// The root-relative spelling still works from anywhere.
	res, err = r.Execute(context.Background(), map[string]any{"path": "/top.txt"})
	if err != nil {
		t.Fatalf("Read(/top.txt) after Cd: %v", err)
	}
	if res.Content != "top" {
		t.Fatalf("content = %q, want top", res.Content)
	}

	// And the path a permission rule sees is the root-relative one, so a rule
	// written against the repository layout keeps matching.
	if got := r.Subject(map[string]any{"path": "leaf.txt"}); got != "internal/tools/leaf.txt" {
		t.Fatalf("Subject = %q, want internal/tools/leaf.txt", got)
	}
}

// A path that escapes the root is still refused after a move — the move
// changes the spelling, never the reach.
func TestReadStillConfinedAfterMove(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if _, err := c.Change("internal/tools"); err != nil {
		t.Fatalf("Change: %v", err)
	}
	r := &Read{Root: root, Cwd: c}
	if _, err := r.Execute(context.Background(), map[string]any{"path": "../../../etc/passwd"}); err == nil {
		t.Fatal("a path escaping the root was allowed after a move")
	}
}

// Glob with no path argument searches the working directory, and reports its
// matches relative to the root so they can be fed straight back to Read.
func TestGlobFollowsWorkingDirectory(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if _, err := c.Change("internal/tools"); err != nil {
		t.Fatalf("Change: %v", err)
	}
	g := &Glob{Root: root, MaxResults: 100, Cwd: c}
	res, err := g.Execute(context.Background(), map[string]any{"pattern": "*.txt"})
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	got := strings.Split(res.Content, "\n")
	want := []string{"internal/tools/leaf.txt", "internal/tools/other.txt"}
	if !equalPaths(got, want) {
		t.Fatalf("Glob = %q, want %q", got, want)
	}
}

// The Cd tool is read-only: it survives the read-only master switch and plan
// mode, because an agent that may only look still needs to look elsewhere.
func TestCdToolIsReadOnly(t *testing.T) {
	cd := &Cd{Cwd: NewCwd(t.TempDir())}
	if Mutates(cd) {
		t.Error("Cd reports as mutating")
	}
	if !cd.Kind().ReadOnly() {
		t.Error("Cd's kind is not read-only")
	}
	if _, ok := Default(t.TempDir(), true).Find("Cd"); !ok {
		t.Error("Cd is missing from the read-only registry")
	}
	if _, ok := Default(t.TempDir(), false).Plan().Find("Cd"); !ok {
		t.Error("Cd is missing from the plan-mode registry")
	}
}

func TestCdToolReportsWhereItLanded(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	cd := &Cd{Cwd: c}

	res, err := cd.Execute(context.Background(), map[string]any{"path": "internal/tools"})
	if err != nil {
		t.Fatalf("Cd: %v", err)
	}
	if res.Content != "working directory: /internal/tools" {
		t.Fatalf("result = %q", res.Content)
	}
	if res.Kind != KindNative {
		t.Fatalf("kind = %q, want %q", res.Kind, KindNative)
	}

	res, err = cd.Execute(context.Background(), map[string]any{"path": "/"})
	if err != nil {
		t.Fatalf("Cd(/): %v", err)
	}
	if res.Content != "working directory: / (session root)" {
		t.Fatalf("result = %q", res.Content)
	}
}

func TestCdToolFailsClosed(t *testing.T) {
	root := cwdTree(t)
	cd := &Cd{Cwd: NewCwd(root)}
	for _, args := range []map[string]any{
		{},
		{"path": ""},
		{"path": "top.txt"},
		{"path": ".."},
		{"path": 42},
	} {
		if _, err := cd.Execute(context.Background(), args); err == nil {
			t.Errorf("Cd(%v) succeeded, want an error", args)
		}
	}
	// A Cd tool with no tracker refuses rather than silently doing nothing.
	if _, err := (&Cd{}).Execute(context.Background(), map[string]any{"path": "internal"}); err == nil {
		t.Error("Cd with no tracker succeeded")
	}
}

// Every tool in the default registry shares one tracker, so a single Cd moves
// all of them. Two trackers would mean two answers to "where am I".
func TestDefaultRegistrySharesOneTracker(t *testing.T) {
	root := cwdTree(t)
	reg := Default(root, false)
	cwd := reg.Cwd()
	if cwd == nil {
		t.Fatal("default registry has no working-directory tracker")
	}

	cd, ok := reg.Find("Cd")
	if !ok {
		t.Fatal("Cd is not registered")
	}
	if _, err := cd.Execute(context.Background(), map[string]any{"path": "internal/tools"}); err != nil {
		t.Fatalf("Cd: %v", err)
	}
	if cwd.Rel() != "internal/tools" {
		t.Fatalf("registry tracker = %q", cwd.Rel())
	}

	read, _ := reg.Find("Read")
	res, err := read.Execute(context.Background(), map[string]any{"path": "leaf.txt"})
	if err != nil {
		t.Fatalf("Read after Cd: %v", err)
	}
	if res.Content != "leaf" {
		t.Fatalf("Read content = %q", res.Content)
	}
}

// Narrowing a registry keeps the same tracker: a plan-mode or read-only view
// of the session must not silently jump back to the root.
func TestRegistryNarrowingKeepsTheTracker(t *testing.T) {
	reg := Default(cwdTree(t), false)
	if reg.ReadOnly().Cwd() != reg.Cwd() {
		t.Error("ReadOnly dropped the tracker")
	}
	if reg.Plan().Cwd() != reg.Cwd() {
		t.Error("Plan dropped the tracker")
	}
	if DefaultWithCaps(cwdTree(t), false, Capabilities{}, repoindex.Index{}).Cwd() == nil {
		t.Error("DefaultWithCaps built a registry with no tracker")
	}
}

// A native tool's path arguments are rebased onto the working directory, and
// an omitted optional path means "here" rather than the root.
func TestNativeToolsFollowWorkingDirectory(t *testing.T) {
	root := cwdTree(t)
	c := NewCwd(root)
	if _, err := c.Change("internal/tools"); err != nil {
		t.Fatalf("Change: %v", err)
	}
	caps := Capabilities{local: map[string]bool{"Cat": true, "LS": true}}
	natives := NativeTools(root, caps, c, repoindex.Index{})
	byName := map[string]Tool{}
	for _, n := range natives {
		byName[n.Definition().Name] = n
	}

	cat, ok := byName["Cat"]
	if !ok {
		t.Skip("cat is not installed")
	}
	res, err := cat.Execute(context.Background(), map[string]any{"path": "leaf.txt"})
	if err != nil {
		t.Fatalf("Cat(leaf.txt) after Cd: %v", err)
	}
	if !strings.Contains(res.Content, "leaf") {
		t.Fatalf("Cat content = %q", res.Content)
	}
	if got := cat.Subject(map[string]any{"path": "leaf.txt"}); got != "internal/tools/leaf.txt" {
		t.Fatalf("Cat subject = %q, want internal/tools/leaf.txt", got)
	}

	ls, ok := byName["LS"]
	if !ok {
		t.Skip("ls is not installed")
	}
	res, err = ls.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("LS with no path after Cd: %v", err)
	}
	if !strings.Contains(res.Content, "leaf.txt") || strings.Contains(res.Content, "top.txt") {
		t.Fatalf("LS listed the wrong directory:\n%s", res.Content)
	}
}

// The Cd description has to state the resolution rule, because it is the one
// rule the model cannot infer from a path argument's schema.
func TestCdDefinitionDocumentsResolution(t *testing.T) {
	d := (&Cd{}).Definition()
	for _, want := range []string{"session root", "relative to the current working directory", "`/`"} {
		if !strings.Contains(d.Description, want) {
			t.Errorf("Cd description missing %q:\n%s", want, d.Description)
		}
	}
	if d.Properties["path"].Description == "" {
		t.Error("Cd path property has no description")
	}
}
