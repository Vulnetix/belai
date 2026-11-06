package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/prompt"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/symindex"
	"github.com/vulnetix/belai/internal/tools"
)

func symbolRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"srv/server.go": "package srv\n\ntype Server struct{}\n\nfunc (s *Server) Handle() {}\n\nfunc helper() {}\n",
		"srv/client.go": "package srv\n\nfunc Dial() {}\n",
		"secret/key.go": "package secret\n\nfunc Rotate() {}\n",
		"web/app.py":    "class Widget:\n    def render(self):\n        pass\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func symbolSession(t *testing.T, root string, set config.Settings, perms permissions.Settings) *Session {
	t.Helper()
	s, err := NewSession(Options{
		Cfg:         run.Config{Provider: "openai", BaseURL: "http://localhost", APIKey: "test", Model: "test"},
		Registry:    tools.Default(root, false),
		Posture:     posture.AllIgnore(),
		Workdir:     root,
		AskDisabled: true,
		Perms:       perms,
		Settings:    set,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func callTool(s *Session, name string, args map[string]any) string {
	return s.executeCall(context.Background(), rolemanager.ToolCall{ID: "c1", Name: name, Args: args}, func(Event) {}, nil)
}

func TestSymbolAndOutlineAnswerFromTheIndexThroughTheSession(t *testing.T) {
	root := symbolRepo(t)
	s := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	out := callTool(s, "Symbol", map[string]any{"name": "Handle"})
	if !strings.Contains(out, "srv/server.go:5  method Server.Handle") {
		t.Fatalf("Symbol:\n%s", out)
	}
	out = callTool(s, "Outline", map[string]any{"file_path": "srv/server.go"})
	for _, want := range []string{"3  struct Server", "5  method Server.Handle", "7  func helper"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Outline missing %q:\n%s", want, out)
		}
	}
	out = callTool(s, "Outline", map[string]any{"file_path": "web/app.py"})
	if !strings.Contains(out, "class Widget") || !strings.Contains(out, "func render") {
		t.Fatalf("Outline of a scanned file:\n%s", out)
	}
	// The cache is written once, privately, beside no repository file.
	cache, _ := config.ProjectSymbolIndexPath(root)
	info, err := os.Stat(cache)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache %s: %v %v", cache, info, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".vulnetix")); err == nil {
		t.Error("the index wrote into the repository")
	}
}

func TestSymbolSeesAFileEditedSinceTheIndexWasBuilt(t *testing.T) {
	root := symbolRepo(t)
	s := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	if out := callTool(s, "Symbol", map[string]any{"name": "Dial"}); !strings.Contains(out, "srv/client.go:3") {
		t.Fatalf("before:\n%s", out)
	}
	// One line is added above the declaration: the row's line must follow.
	p := filepath.Join(root, "srv/client.go")
	if err := os.WriteFile(p, []byte("package srv\n\n// Dial connects.\nfunc Dial() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := callTool(s, "Symbol", map[string]any{"name": "Dial"}); !strings.Contains(out, "srv/client.go:4") {
		t.Fatalf("after an edit:\n%s", out)
	}
	// A file created after the index was built is found by an Outline.
	if err := os.WriteFile(filepath.Join(root, "srv/new.go"), []byte("package srv\n\nfunc Fresh() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := callTool(s, "Outline", map[string]any{"file_path": "srv/new.go"}); !strings.Contains(out, "func Fresh") {
		t.Fatalf("new file:\n%s", out)
	}
}

func TestSymbolRowsFromAWithheldFileAreReplacedByACountThatPointsNowhere(t *testing.T) {
	root := symbolRepo(t)
	s := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	abs := filepath.Join(root, "srv/server.go")
	s.flagged.flag(tools.ReadResultMeta("x", map[string]any{"abs": abs}), rolemanager.SentinelPromptInjection)

	out := callTool(s, "Symbol", map[string]any{"name": "Handle"})
	if strings.Contains(out, "Handle  ") || strings.Contains(out, "method Server.Handle") {
		t.Fatalf("a withheld file's declaration was served:\n%s", out)
	}
	if !strings.Contains(out, "declaration(s) from srv/server.go withheld") {
		t.Fatalf("no withheld line:\n%s", out)
	}
	out = callTool(s, "Outline", map[string]any{"file_path": "srv/server.go"})
	if strings.Contains(out, "struct Server") || !strings.Contains(out, "withheld") {
		t.Fatalf("Outline of a withheld file:\n%s", out)
	}
	for _, bad := range []string{"Grep", "Read", "Glob", "Bash"} {
		if strings.Contains(out, bad) {
			t.Errorf("the placeholder points at %s:\n%s", bad, out)
		}
	}
	// Other files are unaffected.
	if out := callTool(s, "Symbol", map[string]any{"name": "Dial"}); !strings.Contains(out, "srv/client.go:3") {
		t.Fatalf("an unflagged file was dropped:\n%s", out)
	}
}

func TestReadDenyRuleHidesAFileFromSymbolAndOutline(t *testing.T) {
	root := symbolRepo(t)
	perms := permissions.From(nil, nil, []string{"Read(secret/*)"})
	s := symbolSession(t, root, config.Settings{}, perms)
	if out := callTool(s, "Symbol", map[string]any{"name": "Rotate"}); strings.Contains(out, "secret/") {
		t.Fatalf("a denied file's declaration was served:\n%s", out)
	}
	out := callTool(s, "Outline", map[string]any{"file_path": "secret/key.go"})
	if strings.Contains(out, "Rotate") {
		t.Fatalf("Outline of a denied file:\n%s", out)
	}
	if out := callTool(s, "Symbol", map[string]any{"name": "Dial"}); !strings.Contains(out, "srv/client.go") {
		t.Fatalf("an allowed file was hidden:\n%s", out)
	}
}

func TestOutlineAndSymbolRefusePathsOutsideTheRoot(t *testing.T) {
	root := symbolRepo(t)
	s := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	for _, p := range []string{"../x.go", "/etc/hosts"} {
		out := callTool(s, "Outline", map[string]any{"file_path": p})
		if strings.Contains(out, "declaration") && !strings.Contains(out, "error") {
			t.Errorf("%s: %s", p, out)
		}
	}
}

func TestSymbolIndexOffRemovesBothToolsAndTheLine(t *testing.T) {
	root := symbolRepo(t)
	off := false
	s := symbolSession(t, root, config.Settings{SymbolIndex: &config.SymbolIndexSettings{Enabled: &off}}, permissions.Settings{})
	for _, n := range []string{tools.OutlineName, tools.SymbolName} {
		if _, ok := s.registry.Find(n); ok {
			t.Errorf("symbol_index.enabled false left %s registered", n)
		}
	}
	if s.hasSymbolTools() {
		t.Error("hasSymbolTools with the index off")
	}
	on := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	if !on.hasSymbolTools() {
		t.Error("the tools are not on by default")
	}
}

func TestSymbolMaxResultsFollowsTheSetting(t *testing.T) {
	root := symbolRepo(t)
	n := 7
	s := symbolSession(t, root, config.Settings{SymbolIndex: &config.SymbolIndexSettings{MaxResults: &n}}, permissions.Settings{})
	tool, _ := s.registry.Find(tools.SymbolName)
	if got := tool.(*tools.Symbol).MaxResults; got != 7 {
		t.Fatalf("MaxResults = %d, want 7", got)
	}
}

func TestSymbolToolsAreReadOnlyAndAllowedInPlanMode(t *testing.T) {
	root := symbolRepo(t)
	s := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	plan := s.registry.PlanWith(tools.PlanSurface{})
	ro := s.registry.ReadOnlySurface()
	for _, n := range []string{tools.OutlineName, tools.SymbolName} {
		if _, ok := plan.Find(n); !ok {
			t.Errorf("plan mode does not offer %s", n)
		}
		if _, ok := ro.Find(n); !ok {
			t.Errorf("the read_only surface does not offer %s", n)
		}
		if !modes.ToolAllowed(n, nil, true, tools.PlanSurface{}) {
			t.Errorf("plan mode refuses %s", n)
		}
	}
}

func TestSymbolIsInTheWorkDisciplineOnlyWithTheTools(t *testing.T) {
	root := symbolRepo(t)
	s := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	with, err := prompt.System(prompt.Options{WorkDiscipline: true, SymbolTools: s.hasSymbolTools()})
	if err != nil || !strings.Contains(with, "Outline lists a file's declarations") {
		t.Fatalf("line missing: %v\n%s", err, with)
	}
	without, _ := prompt.System(prompt.Options{WorkDiscipline: true})
	if strings.Contains(without, "Outline") || strings.Contains(without, "Symbol finds") {
		t.Fatalf("the line promises tools the session lacks:\n%s", without)
	}
}

func TestSymbolHolderSharesOneIndexAndLoadsTheCache(t *testing.T) {
	root := symbolRepo(t)
	s := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	a := s.symbolAdapter()
	first := a.index(context.Background(), false)
	if first == nil || a.index(context.Background(), false) != first {
		t.Fatal("a second call within the refresh interval rebuilt the index")
	}
	// A new process loads the cache: drop the holder and check the seed.
	symbolHoldersMu.Lock()
	delete(symbolHolders, root)
	symbolHoldersMu.Unlock()
	cache, _ := config.ProjectSymbolIndexPath(root)
	loaded, err := symindex.Load(cache, root, config.DefaultSymbolIndexMaxFiles)
	if err != nil || loaded.Stats().Symbols != first.Stats().Symbols {
		t.Fatalf("cache load: %v", err)
	}
	// A corrupt cache is discarded and rebuilt, never served.
	if err := os.WriteFile(cache, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	rebuilt := a.index(context.Background(), false)
	if rebuilt == nil || rebuilt.Stats().Symbols != first.Stats().Symbols {
		t.Fatal("a corrupt cache broke the index")
	}
}

func TestSymbolIndexUnavailableIsATerseHintAndNeverBlocks(t *testing.T) {
	root := symbolRepo(t)
	s := symbolSession(t, root, config.Settings{}, permissions.Settings{})
	// A root that cannot be read makes the build fail; the call still answers.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	out := callTool(s, "Symbol", map[string]any{"name": "Handle"})
	if !strings.Contains(out, "symbol index unavailable") || !strings.Contains(out, "Grep") {
		t.Fatalf("unavailable:\n%s", out)
	}
}

// A declared name is the repository's own text, so a Symbol result goes to the
// security classifier before the model reads it, as a Read of the file would.
func TestSymbolResultIsAlwaysClassified(t *testing.T) {
	root := symbolRepo(t)
	srv, probe := newClassifyProbeServer("Symbol", `{"name":"Handle"}`)
	defer srv.Close()

	sess, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:   srv.Client(),
		Registry: tools.Default(root, true),
		Posture:  posture.Defaults(),
		Workdir:  root,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Run(context.Background(), "where is Handle declared"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	calls, results := probe.snapshot()
	if calls < 2 {
		t.Fatalf("security classifier called %d times, want admission plus the Symbol result", calls)
	}
	if len(results) != 1 || !strings.Contains(results[0], "method Server.Handle") {
		t.Fatalf("tool result did not reach the model: %q", results)
	}
}
