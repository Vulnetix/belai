package prompt

import (
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/repomap"
)

// WorkspaceBlock renders a harness-computed repository-map block for each
// additional workspace directory. Empty maps are skipped; when no map is
// rendered the function returns "".
func WorkspaceBlock(maps []repomap.Map) string {
	var out []string
	for i, m := range maps {
		block := RepoMapBlock(m)
		if block == "" {
			continue
		}
		root := m.Root
		if root == "" {
			root = m.Module
		}
		out = append(out, fmt.Sprintf("Workspace directory %d (%s):\n%s", i+1, root, block))
	}
	if len(out) == 0 {
		return ""
	}
	return "Additional workspace directory maps:\n" + strings.Join(out, "\n")
}

// RepoMapBlock renders the harness-computed repository map as a fixed-shape
// block for the system prompt. It contains harness-computed facts only — paths,
// counts, detected commands, git metadata and file sizes — and never repository
// file contents. Repository prose reaching the model stays on the RepoRead/Read
// path, which classifies. An empty map renders nothing.
func RepoMapBlock(m repomap.Map) string {
	if m.Module == "" && m.Branch == "" && m.Head == "" && len(m.Languages) == 0 && len(m.Commands.Build) == 0 && len(m.Commands.Test) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Repository map (harness-computed facts, not repository prose):\n")
	if m.Module != "" {
		b.WriteString("root: " + m.Module + "\n")
	}
	if m.Branch != "" || m.Head != "" {
		b.WriteString("git: " + strings.TrimSpace(m.Branch+" "+m.Head))
		if m.Dirty {
			b.WriteString(" (dirty)")
		}
		b.WriteString("\n")
	}
	if len(m.Remotes) > 0 {
		b.WriteString("remotes:\n")
		for _, r := range m.Remotes {
			b.WriteString("- " + r.Name + " " + r.URL + "\n")
		}
	}
	if len(m.Languages) > 0 {
		var langs []string
		for _, l := range m.Languages {
			langs = append(langs, fmt.Sprintf("%s(%d)", l.Ext, l.Files))
		}
		b.WriteString("languages: " + strings.Join(langs, " ") + "\n")
	}
	if len(m.Commands.Build) > 0 {
		b.WriteString("build: " + strings.Join(m.Commands.Build, "; ") + "\n")
	}
	if len(m.Commands.Test) > 0 {
		b.WriteString("test: " + strings.Join(m.Commands.Test, "; ") + "\n")
	}
	if len(m.Commands.Fmt) > 0 {
		b.WriteString("fmt: " + strings.Join(m.Commands.Fmt, "; ") + "\n")
	}
	if len(m.Commands.Lint) > 0 {
		b.WriteString("lint: " + strings.Join(m.Commands.Lint, "; ") + "\n")
	}
	if len(m.Entrypoints) > 0 {
		b.WriteString("entrypoints: " + strings.Join(m.Entrypoints, " ") + "\n")
	}
	if len(m.Layout) > 0 {
		var dirs []string
		for _, d := range m.Layout {
			dirs = append(dirs, fmt.Sprintf("%s(%d)", d.Name, d.Files))
		}
		b.WriteString("layout: " + strings.Join(dirs, " ") + "\n")
	}
	if len(m.AgentsFiles) > 0 {
		var files []string
		for _, f := range m.AgentsFiles {
			files = append(files, fmt.Sprintf("%s(%dB)", f.Name, f.Size))
		}
		b.WriteString("agent files: " + strings.Join(files, " ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
