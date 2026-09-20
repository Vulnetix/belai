// Package repomap computes a harness-generated repository map: languages,
// build/test commands, entrypoints, layout, and the presence (and size only)
// of agent instruction files. It holds harness-computed facts only — never
// repository file contents — which is what lets the map enter the system block
// without violating the untrusted-content invariant.
package repomap

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/gitinfo"
	"github.com/vulnetix/signet/internal/repoindex"
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
	ScannedAt            time.Time
}

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
	m.Dirty = dirty(ctx, info.Root)
	m.Remotes = remotes(ctx, info.Root)
	m.Languages = languages(ctx, info.Root)
	m.Commands = commands(info.Root)
	m.Entrypoints = entrypoints(ctx, info.Root)
	m.Layout = layout(ctx, info.Root)
	m.AgentsFiles = agentsFiles(info.Root)
	return m
}

// dirty reports whether the tree has uncommitted changes, via a bounded
// git status probe.
func dirty(ctx context.Context, root string) bool {
	out := repoindex.RunProbe(ctx, root, "git", "status", "--porcelain")
	return strings.TrimSpace(out) != ""
}

// remotes lists configured git remotes.
func remotes(ctx context.Context, root string) []Remote {
	out := repoindex.RunProbe(ctx, root, "git", "remote", "-v")
	var rs []Remote
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "(fetch)" {
			continue
		}
		rs = append(rs, Remote{Name: fields[0], URL: fields[1]})
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
func commands(root string) Commands {
	var c Commands
	if _, err := os.Stat(filepath.Join(root, "justfile")); err == nil {
		c.Build = append(c.Build, "just build")
		c.Test = append(c.Test, "just check")
		c.Fmt = append(c.Fmt, "just fmt")
		c.Lint = append(c.Lint, "just lint")
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
