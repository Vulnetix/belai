package agentstore

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Format identifies the on-disk dialect of an agent's transcript store.
type Format string

const (
	// FormatJSONLClaude is a Claude Code project transcript: one self-
	// describing JSON object per line (type, message.role, message.content,
	// sessionId, cwd, timestamp, gitBranch).
	FormatJSONLClaude Format = "jsonl-claude"
	// FormatJSONLCodex is a Codex rollout file: line 1 carries session_meta
	// (id and cwd); later lines are response_item messages.
	FormatJSONLCodex Format = "jsonl-codex"
	// FormatJSONLPi is a pi session file: line 1 carries type "session" (id
	// and cwd); later lines are type "message".
	FormatJSONLPi Format = "jsonl-pi"
	// FormatJSONLBelai is a belai session.Entry JSONL file, read through
	// internal/session rather than a new parser.
	FormatJSONLBelai Format = "jsonl-belai"
	// FormatJSONLPrompts is a history.jsonl prompt index.
	FormatJSONLPrompts Format = "jsonl-prompts"
	// FormatSQLiteGoose is the goose sessions SQLite database.
	FormatSQLiteGoose Format = "sqlite-goose"
	// FormatSQLiteOpenCode is the opencode SQLite database.
	FormatSQLiteOpenCode Format = "sqlite-opencode"
	// FormatJSONVSCode is a VS Code Copilot Chat chatSessions JSON file.
	FormatJSONVSCode Format = "json-vscode"
	// FormatJSONGeneric is a tolerated JSON/JSONL store whose exact schema is
	// not guaranteed (currently empty on the survey machine). It degrades to
	// "no sessions" rather than erroring.
	FormatJSONGeneric Format = "json-generic"
	// FormatMarkdown is a memory/rules file; it is handled by the memory
	// search, not the transcript adapters.
	FormatMarkdown Format = "markdown"
)

// Agent is one entry in the static registry of known agent stores.
type Agent struct {
	Name string
	// Sessions are glob patterns for full-transcript files, ~-relative.
	Sessions []string
	// Prompts are glob patterns for history.jsonl prompt indexes, ~-relative.
	Prompts []string
	// Memory are glob patterns for memory/rules files. A pattern not starting
	// with "~" is resolved against the working directory; a "~" pattern is
	// resolved against the home directory.
	Memory []string
	Format Format
}

// knownAgents is the static registry, in deterministic order. Absent paths
// are skipped at probe time; nothing here is read until one of the three
// agent-store tools asks.
var knownAgents = []Agent{
	{
		Name:     "belai",
		Sessions: []string{"~/.vulnetix/belai/sessions/*/*.jsonl"},
		Memory:   []string{".vulnetix/goals", ".vulnetix/prompts", ".vulnetix/plans"},
		Format:   FormatJSONLBelai,
	},
	{
		Name:     "claude-code",
		Sessions: []string{"~/.claude/projects/*/*.jsonl"},
		Prompts:  []string{"~/.claude/history.jsonl"},
		Memory:   []string{"~/.claude/CLAUDE.md", "~/.claude/projects/*/memory/*.md"},
		Format:   FormatJSONLClaude,
	},
	{
		Name:     "codex",
		Sessions: []string{"~/.codex/sessions/**/rollout-*.jsonl", "~/.codex-or/sessions/**/rollout-*.jsonl"},
		Prompts:  []string{"~/.codex/history.jsonl", "~/.codex-or/history.jsonl"},
		Memory:   []string{"~/.codex/memories/**", "~/.codex-or/memories/**", "~/.codex/AGENTS.md", "~/.codex-or/AGENTS.md"},
		Format:   FormatJSONLCodex,
	},
	{
		Name:     "pi",
		Sessions: []string{"~/.pi/agent/sessions/*/*.jsonl"},
		Memory:   []string{"~/.pi/agent/plans/*/*.md"},
		Format:   FormatJSONLPi,
	},
	{
		Name:     "goose",
		Sessions: []string{"~/.local/share/goose/sessions/sessions.db"},
		Memory:   []string{"~/.config/goose/**/*.md"},
		Format:   FormatSQLiteGoose,
	},
	{
		Name:     "opencode",
		Sessions: []string{"~/.local/share/opencode/opencode.db", "~/.opencode/opencode.db"},
		Memory:   []string{"~/.config/opencode/AGENTS.md"},
		Format:   FormatSQLiteOpenCode,
	},
	{
		Name:     "copilot-vscode",
		Sessions: []string{"~/.config/Code*/User/workspaceStorage/*/chatSessions/*.json", "~/.config/Code*/User/globalStorage/emptyWindowChatSessions/*.json"},
		Format:   FormatJSONVSCode,
	},
	{
		Name:     "copilot-cli",
		Sessions: []string{"~/.copilot/session-state/*.json"},
		Format:   FormatJSONGeneric,
	},
	{
		Name:     "antigravity",
		Sessions: []string{"~/.gemini/antigravity-cli/conversations/*"},
		Prompts:  []string{"~/.gemini/antigravity-cli/history.jsonl"},
		Memory:   []string{"~/.gemini/antigravity-cli/brain/*/*.md"},
		Format:   FormatJSONGeneric,
	},
	{
		Name:   "generic",
		Memory: []string{"~/CLAUDE.md", "~/AGENTS.md", "~/.cursorrules", "~/.config/*/AGENTS.md", "CLAUDE.md", "AGENTS.md"},
		Format: FormatMarkdown,
	},
}

// Registry is a lazily probed view of the known agent stores. It is safe for
// concurrent use: the probe runs once under a mutex and every later call reads
// the cached result.
type Registry struct {
	home    string
	workdir string

	mu     sync.Mutex
	probed bool
	status []AgentStatus
	sqlite string
}

// New returns a Registry rooted at home. workdir is the session working
// directory, used for project-relative memory patterns and the default project
// scope of SearchSessions. A home of "" means the user's real home directory.
func New(home, workdir string) *Registry {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return &Registry{home: home, workdir: workdir}
}

// Home returns the home directory the registry expands "~" against.
func (r *Registry) Home() string { return r.home }

// Workdir returns the working directory the registry resolves project-relative
// patterns against.
func (r *Registry) Workdir() string { return r.workdir }

// sqlite3Path resolves the sqlite3 binary once. It returns "" when the binary
// is not on PATH, which marks the SQLite agents unavailable rather than
// failing the call.
func (r *Registry) sqlite3Path() string {
	r.probeOnce()
	return r.sqlite
}

// AgentNames returns the registry's agent names in deterministic order.
func AgentNames() []string {
	out := make([]string, len(knownAgents))
	for i, a := range knownAgents {
		out[i] = a.Name
	}
	return out
}

// HasAgent reports whether name is a known registry agent.
func HasAgent(name string) bool {
	for _, a := range knownAgents {
		if a.Name == name {
			return true
		}
	}
	return false
}

// expandGlobs expands every pattern into concrete, sorted, deduplicated paths.
func expandGlobs(patterns []string, home, workdir string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range patterns {
		got, err := expandGlob(p, home, workdir)
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// expandGlob expands one glob pattern into concrete paths. "~" at the start
// expands to home; a pattern that is not absolute and not "~"-prefixed is
// resolved against workdir. "**" spans zero or more path segments.
func expandGlob(pattern, home, workdir string) ([]string, error) {
	raw := pattern
	switch {
	case raw == "~":
		raw = home
	case strings.HasPrefix(raw, "~/"):
		raw = filepath.Join(home, raw[2:])
	case !filepath.IsAbs(raw):
		raw = filepath.Join(workdir, raw)
	}
	raw = filepath.Clean(raw)

	isAbs := filepath.IsAbs(raw)
	segs := strings.Split(filepath.ToSlash(raw), "/")
	if isAbs && len(segs) > 0 && segs[0] == "" {
		segs = segs[1:]
	}

	first := -1
	for i, s := range segs {
		if strings.ContainsAny(s, "*?[") {
			first = i
			break
		}
	}
	if first < 0 {
		// No metacharacter: a literal path. It is reported only when it exists.
		if _, err := os.Stat(raw); err != nil {
			return nil, nil
		}
		return []string{raw}, nil
	}

	baseParts := segs[:first]
	rest := segs[first:]
	var base string
	if isAbs {
		base = "/" + strings.Join(baseParts, "/")
		if len(baseParts) == 0 {
			base = "/"
		}
	} else {
		base = strings.Join(baseParts, "/")
		if base == "" {
			base = "."
		}
	}

	if _, err := os.Stat(base); err != nil {
		return nil, nil
	}

	var out []string
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return nil
		}
		if matchSegs(rest, strings.Split(filepath.ToSlash(rel), "/")) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, nil
}

// matchSegs matches slash-separated pattern segments against a slash-separated
// path. "**" spans zero or more segments; single segments use path.Match.
func matchSegs(p, n []string) bool {
	if len(p) == 0 {
		return len(n) == 0
	}
	if p[0] == "**" {
		for i := 0; i <= len(n); i++ {
			if matchSegs(p[1:], n[i:]) {
				return true
			}
		}
		return false
	}
	if len(n) == 0 {
		return false
	}
	ok, _ := filepath.Match(p[0], n[0])
	if !ok {
		return false
	}
	return matchSegs(p[1:], n[1:])
}
