// Package repomap computes a harness-generated repository map: languages,
// build/test commands, entrypoints, layout, and the presence (and size only)
// of agent instruction files. It holds harness-computed facts only — never
// repository file contents — which is what lets the map enter the system block
// without violating the untrusted-content invariant.
package repomap

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/repoindex"
)

// Scan bounds: never walk unboundedly.
const (
	scanTimeout = 5 * time.Second
	maxDirs     = 200
	maxFiles    = 2000
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, ".vulnetix": true, ".idea": true, ".vscode": true,
}

// Map is the harness-computed repository map for one commit.
type Map struct {
	Module, Branch, Head string
	Root                 string // absolute filesystem root the map was scanned from
	Dirty                bool
	Remotes              []Remote
	Languages            []LangCount
	Commands             Commands
	Entrypoints          []string
	Layout               []DirSummary
	AgentsFiles          []AgentsFile
	// JustRecipes are the recipe names a justfile declares — identifiers
	// only, never recipe bodies.
	JustRecipes []string
	// Changed is the working tree's changed paths (git status porcelain
	// codes and paths), capped at maxChanged; ChangedTotal is the uncapped
	// count. RefreshStatus updates both per turn so the model starts from
	// what is actually dirty instead of spending calls on git status.
	Changed      []ChangedPath
	ChangedTotal int
	ScannedAt    time.Time
}

// ChangedPath is one `git status --porcelain` row: its two-letter code and
// path.
type ChangedPath struct{ Status, Path string }

// maxChanged caps the changed-path list the map carries.
const maxChanged = 50

// Remote is one configured git remote.
type Remote struct{ Name, URL string }

// LangCount is one detected language, by extension, descending.
type LangCount struct {
	Ext   string
	Files int
}

// Commands holds detected build/test/fmt/lint commands, matched against a
// fixed table — never inferred from prose.
type Commands struct {
	Build []string
	Test  []string
	Fmt   []string
	Lint  []string
}

// DirSummary describes a top-level directory and how many entries it holds.
type DirSummary struct {
	Name  string
	Files int
}

// AgentsFile records the presence and size of an agent instruction file.
type AgentsFile struct {
	Name string
	Size int64
}

// Scan computes the repository map for workdir. It never writes and never
// fetches; a failed detection yields an empty map, and the caller proceeds
// without it (the map is an accelerant, never a gate).
func Scan(ctx context.Context, workdir string) Map {
	start := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	m := Map{ScannedAt: start}
	m.Root = workdir
	info, ok := gitinfo.Detect(workdir)
	if !ok {
		return Map{}
	}
	m.Module = info.Root
	m.Branch = info.Branch
	m.Head = info.Head
	// On a normal branch gitinfo only records the branch name; the short HEAD
	// SHA is the stable commit key the registry compares against.
	if m.Head == "" {
		m.Head = repoindex.RunProbe(ctx, info.Root, "git", "rev-parse", "--short", "HEAD")
	}
	m.RefreshStatus(ctx)
	m.Remotes = remotes(ctx, info.Root)
	m.Languages = languages(ctx, info.Root)
	m.JustRecipes = justRecipes(info.Root)
	m.Commands = commands(info.Root, m.JustRecipes)
	m.Entrypoints = entrypoints(ctx, info.Root)
	m.Layout = layout(ctx, info.Root)
	m.AgentsFiles = agentsFiles(info.Root)
	return m
}

// RefreshStatus re-probes the working tree (one bounded `git status
// --porcelain --branch` and one `git rev-parse --short HEAD`) and updates
// Branch, Head, Dirty, Changed and ChangedTotal. It is cheap enough to run
// before every turn, which keeps the facts current after the session's own
// edits and commits.
func (m *Map) RefreshStatus(ctx context.Context) {
	if m == nil || m.Module == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out := repoindex.RunProbe(ctx, m.Module, "git", "status", "--porcelain", "--branch")
	if branch := parseBranch(out); branch != "" {
		m.Branch = branch
	}
	if head := repoindex.RunProbe(ctx, m.Module, "git", "rev-parse", "--short", "HEAD"); head != "" {
		m.Head = head
	}
	m.Changed, m.ChangedTotal = parseStatus(out)
	m.Dirty = m.ChangedTotal > 0
}

// parseBranch reads the branch out of a `--branch` header line
// ("## main...origin/main [ahead 1]"). A detached HEAD reports no branch.
func parseBranch(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	rest, ok := strings.CutPrefix(line, "## ")
	if !ok || strings.HasPrefix(rest, "HEAD (no branch)") {
		return ""
	}
	// A repository with no commits yet reports "No commits yet on main".
	rest = strings.TrimPrefix(rest, "No commits yet on ")
	rest, _, _ = strings.Cut(rest, "...")
	rest, _, _ = strings.Cut(rest, " ")
	return rest
}

// parseStatus reads `git status --porcelain` output into capped rows. A
// `--branch` header line is skipped.
func parseStatus(out string) ([]ChangedPath, int) {
	var rows []ChangedPath
	total := 0
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 || strings.HasPrefix(line, "## ") {
			continue
		}
		total++
		if len(rows) >= maxChanged {
			continue
		}
		rows = append(rows, ChangedPath{Status: strings.TrimSpace(line[:2]), Path: strings.TrimSpace(line[3:])})
	}
	return rows, total
}

// remotes lists configured git remotes.
func remotes(ctx context.Context, root string) []Remote {
	out := repoindex.RunProbe(ctx, root, "git", "remote", "-v")
	var rs []Remote
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// `origin <url> (fetch)`: the marker is the third field.
		if len(fields) < 3 || fields[2] != "(fetch)" {
			continue
		}
		rs = append(rs, Remote{Name: fields[0], URL: redactRemote(fields[1])})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
	return rs
}

// languages counts file extensions under root, bounded and skip-dir aware.
func languages(ctx context.Context, root string) []LangCount {
	counts := map[string]int{}
	filesSeen := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		filesSeen++
		if filesSeen > maxFiles {
			return nil
		}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(d.Name())), ".")
		if ext == "" {
			ext = "(none)"
		}
		counts[ext]++
		return nil
	})
	var out []LangCount
	for ext, n := range counts {
		out = append(out, LangCount{Ext: ext, Files: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Ext < out[j].Ext
	})
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// commands detects build/test/fmt/lint commands from a fixed file table.
// A justfile contributes only the recipes it actually declares, so the map
// never advertises `just lint` to a repo that has no lint recipe.
func commands(root string, recipes []string) Commands {
	var c Commands
	if len(recipes) > 0 {
		has := map[string]bool{}
		for _, r := range recipes {
			has[r] = true
		}
		pick := func(dst *[]string, names ...string) {
			for _, n := range names {
				if has[n] {
					*dst = append(*dst, "just "+n)
				}
			}
		}
		pick(&c.Build, "build")
		pick(&c.Test, "check", "test")
		pick(&c.Fmt, "fmt")
		pick(&c.Lint, "lint", "vet")
	}
	if _, err := os.Stat(filepath.Join(root, "Makefile")); err == nil {
		c.Build = append(c.Build, "make build")
		c.Test = append(c.Test, "make test")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		c.Build = append(c.Build, "go build ./...")
		c.Test = append(c.Test, "go test ./...")
		c.Fmt = append(c.Fmt, "gofmt -w .")
		c.Lint = append(c.Lint, "go vet ./...")
	}
	if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); err == nil {
		c.Build = append(c.Build, "cargo build")
		c.Test = append(c.Test, "cargo test")
	}
	if _, err := os.Stat(filepath.Join(root, "pyproject.toml")); err == nil {
		c.Build = append(c.Build, "pip install -e .")
		c.Test = append(c.Test, "pytest")
	}
	if _, err := os.Stat(filepath.Join(root, "package.json")); err == nil {
		c.Build = append(c.Build, "npm run build")
		c.Test = append(c.Test, "npm test")
		c.Lint = append(c.Lint, "npm run lint")
	}
	return c
}

// entrypoints finds likely program entrypoints: a top-level main.go and
// main.go files under cmd/.
func entrypoints(ctx context.Context, root string) []string {
	var out []string
	if _, err := os.Stat(filepath.Join(root, "main.go")); err == nil {
		out = append(out, "main.go")
	}
	cmdDir := filepath.Join(root, "cmd")
	if _, err := os.Stat(cmdDir); err == nil {
		_ = filepath.WalkDir(cmdDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || ctx.Err() != nil {
				return nil
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == "main.go" {
				rel, _ := filepath.Rel(root, path)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	sort.Strings(out)
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}

// layout summarises top-level directories and their file counts.
func layout(ctx context.Context, root string) []DirSummary {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []DirSummary
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		if !e.IsDir() || skipDirs[e.Name()] || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		files := 0
		_ = filepath.WalkDir(filepath.Join(root, e.Name()), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			files++
			if files > maxFiles {
				return filepath.SkipAll
			}
			return nil
		})
		out = append(out, DirSummary{Name: e.Name(), Files: files})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// agentsFiles records agent instruction files by presence and size only.
func agentsFiles(root string) []AgentsFile {
	var out []AgentsFile
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		fi, err := os.Stat(filepath.Join(root, name))
		if err == nil {
			out = append(out, AgentsFile{Name: name, Size: fi.Size()})
		}
	}
	return out
}

// maxRecipes caps the recipe names the map carries.
const maxRecipes = 40

// justRecipes returns the recipe names a justfile at root declares, in file
// order. Only header identifiers are read — a recipe header is an
// unindented line `name [params]:` that is not a `:=` assignment — so the map
// never carries recipe bodies or comments.
func justRecipes(root string) []string {
	var data []byte
	for _, name := range []string{"justfile", "Justfile", ".justfile"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err == nil {
			data = b
			break
		}
	}
	if data == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' || line[0] == '[' {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || strings.HasPrefix(line[colon:], ":=") {
			continue
		}
		head := strings.Fields(line[:colon])
		if len(head) == 0 {
			continue
		}
		name := strings.TrimPrefix(head[0], "@")
		if head[0] == "set" || head[0] == "alias" || head[0] == "import" || head[0] == "mod" || head[0] == "export" || !isRecipeName(name) || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= maxRecipes {
			break
		}
	}
	return out
}

// isRecipeName reports whether s is a just recipe identifier.
func isRecipeName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && (r >= '0' && r <= '9' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// redactRemote strips credentials from a remote URL. Remotes enter the system
// block, so `https://user:token@host/repo` must never reach a provider; the
// scp form `git@host:repo` carries no secret and is kept.
func redactRemote(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
