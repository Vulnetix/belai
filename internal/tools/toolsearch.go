package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Deferred tool loading. A registry with the full native catalogue, the
// agent-store and repo tools and a few MCP servers is 80+ tool definitions —
// ~57 KB of JSON, most of every request, prefilled on every call of the loop
// although a typical turn uses five of them. The session advertises a core
// set in full and names the rest in the sealed tools briefing; ToolSearch
// loads a deferred tool's definition into the request on demand. The name and
// argument shape are Claude Code's, which models are trained on.
//
// Deferral changes what is advertised, never what may run: a deferred tool is
// still registered, still permission-checked, and still executes if the model
// calls it by name without loading it first. The result names tools only —
// never a description — so MCP server text still reaches the model only
// through the sealed tools briefing and the definitions it already governs.

// ToolSearchName is the tool's name.
const ToolSearchName = "ToolSearch"

// CoreTools are advertised in full on every surface that has them. Everything
// else on a surface is deferred behind ToolSearch.
var CoreTools = []string{
	"Read", "Write", "Edit", "Bash", "Grep", "Glob", "WebFetch", "WebSearch",
	"update_plan", TodoName, "ExitPlanMode", "AskUserQuestion", "Task", "Skill", CodeName,
	KanbanSearchName, KanbanUpdateName, KanbanMoveName, KanbanAddName, KanbanHandoffName, KanbanVerdictName, KanbanGateName, KanbanContractName,
	"SubAgentLog", "ProcessRestart", BashOutputName, KillShellName, ScreenshotName, ToolSearchName, ReadResultName,
}

// IsCoreTool reports whether name is always advertised in full.
func IsCoreTool(name string) bool {
	for _, c := range CoreTools {
		if strings.EqualFold(c, name) {
			return true
		}
	}
	return false
}

// ToolCatalog is what ToolSearch searches and loads from: the deferred tools
// of the current surface, and a loader that puts named tools on it.
type ToolCatalog interface {
	// Deferred returns the definitions of the tools not loaded yet.
	Deferred() []Definition
	// Load puts the named tools on the advertised surface and returns the
	// names it loaded.
	Load(names []string) []string
}

// ToolSearch loads deferred tool definitions.
type ToolSearch struct {
	Catalog ToolCatalog
}

// Definition describes the tool with Claude Code's argument shape.
func (ToolSearch) Definition() Definition {
	return Definition{
		Name: ToolSearchName,
		Description: "Load the full definitions of deferred tools so you can call them. The tools briefing lists every deferred tool by name. " +
			"Use \"select:Name1,Name2\" to load tools by exact name, or keywords to load the best matches. Loaded tools stay available for the rest of the session. " +
			"It also finds installed skills by name: a skill it names is loaded by calling the Skill tool.",
		Properties: map[string]Property{
			"query":       {Type: "string", Description: "\"select:<Name>[,<Name>…]\" for exact names, or keywords describing the capability you need."},
			"max_results": {Type: "integer", Description: "For a keyword query, how many tools to load at most (default 5)."},
		},
		Required: []string{"query"},
	}
}

// Kind is the harness-composed confirmation.
func (ToolSearch) Kind() Kind { return KindToolSearch }

// Subject has no permission subject.
func (ToolSearch) Subject(map[string]any) string { return "" }

// Mutates reports false: it changes what is advertised, not the workspace.
func (ToolSearch) Mutates() bool { return false }

// Execute resolves the query against the deferred tools and the installed
// skills. Tools are loaded into the request; a skill is named, and the model
// loads it with the Skill tool. The result names candidates only.
//
// "select:" names are matched exactly and never go through a ranker. A keyword
// query is ranked deterministically (BM25 over name and description); a
// catalog that offers a SearchRanker may then refine that list, and its failure
// leaves the deterministic list as it was.
func (t ToolSearch) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if t.Catalog == nil {
		return Result{Kind: KindToolSearch, Content: "no deferred tools: every tool is already loaded"}, nil
	}
	query, _ := argString(args, "query")
	query = strings.TrimSpace(query)
	if query == "" {
		return Result{}, fmt.Errorf("query is required: \"select:<Name>\" or keywords")
	}
	limit := 5
	if n, ok := argInt64(args, "max_results"); ok && n > 0 {
		limit = int(min(n, 25))
	}
	deferred := t.Catalog.Deferred()
	var skills []Candidate
	if sc, ok := t.Catalog.(SkillCatalog); ok {
		skills = sc.Skills()
	}
	pool := make([]Candidate, 0, len(deferred)+len(skills))
	for _, d := range deferred {
		pool = append(pool, Candidate{Name: d.Name, Description: d.Description})
	}
	pool = append(pool, skills...)

	var want []Candidate
	if rest, ok := strings.CutPrefix(query, "select:"); ok {
		for _, n := range strings.Split(rest, ",") {
			n = strings.TrimSpace(n)
			for _, c := range pool {
				if strings.EqualFold(c.Name, n) {
					want = append(want, c)
				}
			}
		}
	} else {
		ranked := RankCandidates(pool, query)
		want = ranked[:min(limit, len(ranked))]
		if rk, ok := t.Catalog.(SearchRanker); ok {
			if refined, ok := rk.Rank(ctx, query, want, pool, limit); ok {
				want = refined
			}
		}
	}

	var toolNames, skillNames []string
	for _, c := range want {
		if c.Skill {
			skillNames = append(skillNames, c.Name)
		} else {
			toolNames = append(toolNames, c.Name)
		}
	}
	loaded := t.Catalog.Load(toolNames)
	if len(loaded) == 0 && len(skillNames) == 0 {
		names := make([]string, 0, len(deferred))
		for _, d := range deferred {
			names = append(names, d.Name)
		}
		sort.Strings(names)
		what := "tool"
		if len(skills) > 0 {
			what = "tool or skill"
		}
		if len(names) == 0 && len(skills) == 0 {
			return Result{Kind: KindToolSearch, Content: "no deferred tools remain: every tool is already loaded"}, nil
		}
		msg := fmt.Sprintf("no deferred %s matches %q", what, sanitizeToolQuery(query))
		if len(names) > 0 {
			msg += "; deferred tools: " + strings.Join(names, ", ")
		}
		return Result{Kind: KindToolSearch, Content: msg}, nil
	}
	var parts []string
	if len(loaded) > 0 {
		parts = append(parts, fmt.Sprintf("loaded %d tool(s): %s. Their full definitions are now in your tool list; call them directly.", len(loaded), strings.Join(loaded, ", ")))
	}
	if len(skillNames) > 0 {
		parts = append(parts, fmt.Sprintf("found %d skill(s): %s. Load a skill by calling the Skill tool with its name.", len(skillNames), strings.Join(skillNames, ", ")))
	}
	return Result{Kind: KindToolSearch, Content: strings.Join(parts, " ")}, nil
}

// sanitizeToolQuery bounds the echoed query to identifier-ish characters.
func sanitizeToolQuery(q string) string {
	var b strings.Builder
	for _, r := range q {
		if b.Len() >= 80 {
			break
		}
		if r == ' ' || r == '_' || r == '-' || r == ':' || r == ',' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DeferSet splits a surface's tools into the advertised part and the deferred
// part, given the names already loaded. The advertised part keeps the
// surface's order for the core tools and appends loaded tools in load order,
// so loading a tool extends the request's tool list rather than reshuffling
// its cacheable prefix.
func DeferSet(defs []Definition, loaded []string) (advertised, deferred []Definition) {
	byName := make(map[string]Definition, len(defs))
	for _, d := range defs {
		if IsCoreTool(d.Name) {
			advertised = append(advertised, d)
			continue
		}
		byName[strings.ToLower(d.Name)] = d
	}
	for _, n := range loaded {
		if d, ok := byName[strings.ToLower(n)]; ok {
			advertised = append(advertised, d)
			delete(byName, strings.ToLower(n))
		}
	}
	for _, d := range defs {
		if _, ok := byName[strings.ToLower(d.Name)]; ok {
			deferred = append(deferred, d)
		}
	}
	return advertised, deferred
}

var (
	_ Tool    = ToolSearch{}
	_ Mutator = ToolSearch{}
)
