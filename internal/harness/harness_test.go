package harness

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

	validKinds = map[string]bool{Command: true, Skill: true, Prompt: true, Agent: true, Document: true}

	validFormats = map[string]bool{
		"claude-code": true, "cursor": true, "codex": true, "gemini-cli": true,
		"opencode": true, "windsurf": true, "copilot": true, "cline": true, "generic-md": true,
	}
)

func TestRegistryLoads(t *testing.T) {
	if n := len(All()); n < 65 {
		t.Fatalf("registry has %d harnesses, want at least 65", n)
	}
}

func TestIDsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, h := range All() {
		if !idPattern.MatchString(h.ID) {
			t.Errorf("id %q is not lower-case kebab", h.ID)
		}
		if prev, dup := seen[h.ID]; dup {
			t.Errorf("id %q is also %s", h.ID, prev)
		}
		seen[h.ID] = "an id"
		for _, a := range h.Aliases {
			if !idPattern.MatchString(a) {
				t.Errorf("%s: alias %q is not lower-case kebab", h.ID, a)
			}
			if prev, dup := seen[a]; dup {
				t.Errorf("%s: alias %q is also %s", h.ID, a, prev)
			}
			seen[a] = "an alias of " + h.ID
		}
	}
}

func TestSorted(t *testing.T) {
	hs := All()
	if !sort.SliceIsSorted(hs, func(i, j int) bool { return hs[i].ID < hs[j].ID }) {
		t.Error("harnesses are not sorted by id; regenerate with tools/gen-harnesses")
	}
}

func TestEachHarnessIsWellFormed(t *testing.T) {
	for _, h := range All() {
		if strings.TrimSpace(h.Name) == "" {
			t.Errorf("%s: no name", h.ID)
		}
		if !validFormats[h.Format] {
			t.Errorf("%s: format %q is not a known agentimport format", h.ID, h.Format)
		}
		if len(h.Dirs) == 0 {
			t.Errorf("%s: no directories", h.ID)
		}
		for _, d := range h.Detect {
			checkUser(t, h.ID+" detect", d)
		}
		for kind, kd := range h.Dirs {
			where := h.ID + " " + kind
			if !validKinds[kind] {
				t.Errorf("%s: unknown kind", where)
				continue
			}
			if len(kd.User)+len(kd.Project) == 0 {
				t.Errorf("%s: no directories", where)
			}
			for _, d := range kd.User {
				checkUser(t, where, d)
			}
			for _, d := range kd.Project {
				checkProject(t, where, d)
			}
			checkShape(t, where, kind, kd)
		}
	}
}

func checkUser(t *testing.T, where, d string) {
	t.Helper()
	if !strings.HasPrefix(d, "~/") || len(d) == 2 {
		t.Errorf("%s: user dir %q must start with ~/", where, d)
		return
	}
	rest := d[2:]
	if path.Clean(rest) != rest || strings.HasPrefix(rest, "/") || containsDotDot(rest) {
		t.Errorf("%s: user dir %q is not clean", where, d)
	}
}

func checkProject(t *testing.T, where, d string) {
	t.Helper()
	if d == "" || strings.HasPrefix(d, "/") || strings.HasPrefix(d, "~") || containsDotDot(d) || path.Clean(d) != d {
		t.Errorf("%s: project dir %q must be a clean relative path", where, d)
	}
}

func containsDotDot(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func checkShape(t *testing.T, where, kind string, kd KindDirs) {
	t.Helper()
	want := map[string]string{Skill: SkillDir, Document: DocFile, Command: MDFile, Agent: MDFile, Prompt: MDFile}[kind]
	switch kd.Shape {
	case want, Unsupported:
	default:
		t.Errorf("%s: shape %q, want %q or %q", where, kd.Shape, want, Unsupported)
	}
	if kd.Shape == DocFile && len(kd.Files) == 0 {
		t.Errorf("%s: doc-file shape names no files", where)
	}
	if kd.Shape != DocFile && kd.Shape != Unsupported && len(kd.Files) > 0 {
		t.Errorf("%s: files only belong to the doc-file shape", where)
	}
	seen := map[string]bool{}
	for _, f := range kd.Files {
		if f == "" || strings.ContainsAny(f, `/\*?[`) || f == "." || f == ".." {
			t.Errorf("%s: %q is not a file name", where, f)
		}
		if seen[f] {
			t.Errorf("%s: file %q listed twice", where, f)
		}
		seen[f] = true
	}
}

// Two harnesses reading one command, agent or prompt directory would list every
// file twice under different harnesses, so it is a data error. Skill and
// document directories are shared on purpose (~/.agents/skills, AGENTS.md).
func TestNoCommandAgentPromptDirCollisions(t *testing.T) {
	owner := map[string]string{}
	for _, h := range All() {
		for _, kind := range []string{Command, Agent, Prompt} {
			kd, ok := h.Dirs[kind]
			if !ok {
				continue
			}
			for _, d := range append(append([]string{}, kd.User...), kd.Project...) {
				key := kind + " " + d
				if prev, dup := owner[key]; dup && prev != h.ID {
					t.Errorf("%s dir %q is used by %s and %s", kind, d, prev, h.ID)
				}
				owner[key] = h.ID
			}
		}
	}
}

func TestKnownConventions(t *testing.T) {
	type want struct {
		id, kind, shape, dir string
	}
	for _, w := range []want{
		{"claude-code", Command, MDFile, "~/.claude/commands"},
		{"claude-code", Agent, MDFile, "~/.claude/agents"},
		{"claude-code", Skill, SkillDir, "~/.claude/skills"},
		{"claude-code", Document, DocFile, "~/.claude"},
		{"cursor", Command, MDFile, "~/.cursor/commands"},
		{"cursor", Command, MDFile, ".cursor/commands"},
		{"codex", Prompt, MDFile, "~/.codex/prompts"},
		{"codex", Skill, SkillDir, "~/.agents/skills"},
		{"gemini-cli", Command, Unsupported, "~/.gemini/commands"},
		{"opencode", Command, MDFile, "~/.config/opencode/command"},
		{"opencode", Agent, MDFile, ".opencode/agent"},
		{"windsurf", Command, MDFile, ".windsurf/workflows"},
		{"github-copilot", Prompt, MDFile, ".github/prompts"},
		{"github-copilot", Agent, MDFile, ".github/agents"},
	} {
		h, ok := ByID(w.id)
		if !ok {
			t.Errorf("%s: not in the registry", w.id)
			continue
		}
		kd := h.Dirs[w.kind]
		if kd.Shape != w.shape {
			t.Errorf("%s %s: shape %q, want %q", w.id, w.kind, kd.Shape, w.shape)
		}
		found := false
		for _, d := range append(append([]string{}, kd.User...), kd.Project...) {
			found = found || d == w.dir
		}
		if !found {
			t.Errorf("%s %s: %q missing from %v %v", w.id, w.kind, w.dir, kd.User, kd.Project)
		}
	}
	if h, _ := ByID("claude-code"); h.Format != "claude-code" {
		t.Errorf("claude-code format = %q", h.Format)
	}
}

func TestByIDResolvesAliases(t *testing.T) {
	h, ok := ByID("openai-codex")
	if !ok || h.ID != "codex" {
		t.Fatalf("ByID(openai-codex) = %q, %v; want codex", h.ID, ok)
	}
	if _, ok := ByID("no-such-harness"); ok {
		t.Fatal("ByID found a harness that does not exist")
	}
}

func TestInstalledAndExpand(t *testing.T) {
	home := t.TempDir()
	h, _ := ByID("claude-code")
	if h.Installed(home) {
		t.Fatal("empty home reports claude-code installed")
	}
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !h.Installed(home) {
		t.Fatal("~/.claude present but claude-code not installed")
	}
	if got := Expand(home, "~/.claude/commands"); got != filepath.Join(home, ".claude", "commands") {
		t.Fatalf("Expand = %q", got)
	}
	if got := Expand(home, "~"); got != home {
		t.Fatalf("Expand(~) = %q", got)
	}
}

// docsPath is the reference page; every harness must be listed there.
func TestDocsListEveryHarness(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "harnesses.md"))
	if err != nil {
		t.Skipf("docs/harnesses.md not readable: %v", err)
	}
	for _, h := range All() {
		if !bytes.Contains(b, []byte("`"+h.ID+"`")) {
			t.Errorf("docs/harnesses.md does not list `%s`", h.ID)
		}
	}
}

// --- parity with the cli repository ---------------------------------------

func cliDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("HARNESS_CLI_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", "..", "cli")
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "agent", "hosts.go")); err != nil {
		t.Skipf("cli repository not present at %s", dir)
	}
	return dir
}

type overlayIDs struct {
	IDMap   map[string]string `json:"idMap"`
	Exclude map[string]string `json:"exclude"`
}

func readOverlay(t *testing.T) overlayIDs {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "gen-harnesses", "overlay.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ov overlayIDs
	if err := json.Unmarshal(b, &ov); err != nil {
		t.Fatal(err)
	}
	return ov
}

// goStringKeys returns the string literals of a package-level var's composite
// literal: map keys for a map, the ID field for a struct slice.
func goIDs(t *testing.T, file, varName string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			if len(vs.Names) == 0 || vs.Names[0].Name != varName || len(vs.Values) == 0 {
				continue
			}
			cl, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, el := range cl.Elts {
				switch e := el.(type) {
				case *ast.KeyValueExpr: // map entry
					if bl, ok := e.Key.(*ast.BasicLit); ok {
						s, _ := strconv.Unquote(bl.Value)
						ids = append(ids, s)
					}
				case *ast.CompositeLit: // struct element with an ID field
					for _, kv := range e.Elts {
						k, ok := kv.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if id, ok := k.Key.(*ast.Ident); ok && id.Name == "ID" {
							if bl, ok := k.Value.(*ast.BasicLit); ok {
								s, _ := strconv.Unquote(bl.Value)
								ids = append(ids, s)
							}
						}
					}
				}
			}
		}
	}
	if len(ids) == 0 {
		t.Fatalf("%s: no ids found in %s", file, varName)
	}
	return ids
}

// toolsWithFiles are the tools.json harnesses that record at least one skills,
// commands, agents or prompts directory or one fixed instruction file.
func toolsWithFiles(t *testing.T, file string, ov overlayIDs) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools []struct {
			ID    string              `json:"id"`
			Type  string              `json:"type"`
			Paths map[string][]string `json:"paths"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tool := range doc.Tools {
		if tool.Type != "cli-agent" && tool.Type != "ide" && tool.Type != "ide-extension" {
			continue
		}
		if _, skip := ov.Exclude[tool.ID]; skip {
			continue
		}
		has := false
		for cat, globs := range tool.Paths {
			for _, g := range globs {
				switch cat {
				case "skills", "commands", "agents", "prompts":
					d, ok := strings.CutSuffix(g, "/**")
					has = has || (ok && !strings.ContainsAny(d, "*?["))
				case "instructions":
					has = has || !strings.ContainsAny(g, "*?[")
				}
			}
		}
		if has {
			ids = append(ids, tool.ID)
		}
	}
	return ids
}

func TestEveryCLIHarnessIsRegistered(t *testing.T) {
	cli := cliDir(t)
	ov := readOverlay(t)
	var want []string
	want = append(want, goIDs(t, filepath.Join(cli, "internal", "agent", "hosts.go"), "Hosts")...)
	agentDirs := goIDs(t, filepath.Join(cli, "cmd", "skills.go"), "agentDirs")
	if len(agentDirs) < 60 {
		t.Fatalf("agentDirs has %d ids, the parser lost some", len(agentDirs))
	}
	want = append(want, agentDirs...)
	want = append(want, toolsWithFiles(t, filepath.Join(cli, "internal", "aibom", "catalog", "tools.json"), ov)...)
	for _, id := range want {
		if c, ok := ov.IDMap[id]; ok {
			id = c
		}
		if _, ok := ByID(id); !ok {
			t.Errorf("cli harness %q is missing from the registry; run tools/gen-harnesses", id)
		}
	}
}

func TestGeneratorIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the generator")
	}
	cli := cliDir(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if cli, err = filepath.Abs(cli); err != nil {
		t.Fatal(err)
	}
	var outputs [2][]byte
	for i := range outputs {
		out := filepath.Join(t.TempDir(), "harnesses.json")
		cmd := exec.Command("go", "run", "./tools/gen-harnesses", "-cli", cli, "-out", out)
		cmd.Dir = root
		done := make(chan struct{})
		var combined []byte
		var runErr error
		go func() {
			combined, runErr = cmd.CombinedOutput()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Minute):
			_ = cmd.Process.Kill()
			t.Fatal("generator timed out")
		}
		if runErr != nil {
			t.Fatalf("generator: %v\n%s", runErr, combined)
		}
		if outputs[i], err = os.ReadFile(out); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(outputs[0], outputs[1]) {
		t.Fatal("two generator runs gave different bytes")
	}
	if !bytes.Equal(outputs[0], raw) {
		t.Fatal("internal/harness/harnesses.json is stale: run `go run ./tools/gen-harnesses -cli ../cli -out internal/harness/harnesses.json`")
	}
}
