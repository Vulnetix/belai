package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTokenizeSplitsCamelSnakeAndStems(t *testing.T) {
	cases := map[string]string{
		"ReadResult":                      "read result",
		"mcp__vulnetix__advisories":       "mcp vulnetix advisory",
		"Reading the files, please":       "read file",
		"GitHub pull_requests & Issues":   "git hub pull request issue",
		"HTTP2Server":                     "http2 server",
		"a I x":                           "",
		"Transforming JSON with a filter": "transform json filter",
	}
	for in, want := range cases {
		if got := strings.Join(tokenize(in), " "); got != want {
			t.Errorf("tokenize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStemKeepsShortWords(t *testing.T) {
	for in, want := range map[string]string{"is": "is", "bus": "bus", "sing": "sing", "reads": "read", "parsing": "pars", "files": "file", "boxes": "box", "queries": "query", "class": "class", "matches": "match", "issues": "issue", "filter": "filter", "edited": "edit"} {
		if got := stem(in); got != want {
			t.Errorf("stem(%q) = %q, want %q", in, got, want)
		}
	}
}

func cands() []Candidate {
	return []Candidate{
		{Name: "GH", Description: "Query GitHub (read-only): pull requests, issues and workflow runs."},
		{Name: "JQ", Description: "Transform JSON using a jq filter."},
		{Name: "WC", Description: "Count lines, words and bytes."},
		{Name: "mcp__vulnetix__advisories", Description: "Vendor advisories for a package."},
		{Name: "pdf-tools", Description: "Fill and merge PDF forms.", Skill: true},
		{Name: "git-ops", Description: "Rebase branches, resolve conflicts and open pull requests.", Skill: true},
	}
}

func rankedNames(cs []Candidate) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestRankCandidatesFindsByNameAndDescription(t *testing.T) {
	c := cands()
	cases := map[string]string{
		"pull requests":         "GH,git-ops",
		"json filter":           "JQ",
		"count words":           "WC",
		"advisory for packages": "mcp__vulnetix__advisories",
		"rebase my branch":      "git-ops",
		"merge pdf":             "pdf-tools",
	}
	for q, want := range cases {
		got := rankedNames(RankCandidates(c, q))
		if !strings.HasPrefix(got, want) {
			t.Errorf("RankCandidates(%q) = %s, want it to start with %s", q, got, want)
		}
	}
	if got := RankCandidates(c, "quantum entanglement"); len(got) != 0 {
		t.Errorf("an unrelated query matched: %v", got)
	}
	if len(RankCandidates(c, "the a of")) != 0 || len(RankCandidates(nil, "x")) != 0 {
		t.Error("empty query or pool must rank nothing")
	}
}

func TestRankCandidatesNameHitOutranksDescriptionHit(t *testing.T) {
	c := []Candidate{
		{Name: "Alpha", Description: "works with diff output"},
		{Name: "Diff", Description: "Compare two files."},
	}
	if got := rankedNames(RankCandidates(c, "diff")); got != "Diff,Alpha" {
		t.Fatalf("got %s", got)
	}
}

func TestRankCandidatesIsDeterministic(t *testing.T) {
	c := []Candidate{
		{Name: "B", Description: "search text"}, {Name: "A", Description: "search text"}, {Name: "C", Description: "search text"},
	}
	first := rankedNames(RankCandidates(c, "search text"))
	for i := 0; i < 20; i++ {
		if got := rankedNames(RankCandidates(c, "search text")); got != first {
			t.Fatalf("ranking changed: %s vs %s", got, first)
		}
	}
	if first != "A,B,C" {
		t.Fatalf("ties should sort by name, got %s", first)
	}
}

func TestCandidateKeySeparatesToolsFromSkills(t *testing.T) {
	if (Candidate{Name: "x"}).Key() == (Candidate{Name: "x", Skill: true}).Key() {
		t.Fatal("a tool and a skill with one name share a key")
	}
}

// fullCatalog is a catalog that also searches skills and refines with a ranker.
type fullCatalog struct {
	fakeCatalog
	skills   []Candidate
	rank     func(query string, det, pool []Candidate, limit int) ([]Candidate, bool)
	gotDet   []Candidate
	gotPool  []Candidate
	rankSeen int
}

func (f *fullCatalog) Skills() []Candidate { return f.skills }
func (f *fullCatalog) Rank(_ context.Context, q string, det, pool []Candidate, limit int) ([]Candidate, bool) {
	f.rankSeen++
	f.gotDet, f.gotPool = det, pool
	if f.rank == nil {
		return nil, false
	}
	return f.rank(q, det, pool, limit)
}

func newFull() *fullCatalog {
	return &fullCatalog{
		fakeCatalog: fakeCatalog{deferred: []Definition{
			{Name: "GH", Description: "Query GitHub: pull requests and issues."},
			{Name: "JQ", Description: "Transform JSON using a jq filter. SECRET-TOOL-TEXT"},
		}},
		skills: []Candidate{
			{Name: "git-ops", Description: "Rebase branches and open pull requests. SECRET-SKILL-TEXT", Skill: true},
			{Name: "pdf-tools", Description: "Fill and merge PDF forms.", Skill: true},
		},
	}
}

func TestToolSearchFindsSkillsAndNamesThemOnly(t *testing.T) {
	cat := newFull()
	ts := ToolSearch{Catalog: cat}
	res, err := ts.Execute(context.Background(), map[string]any{"query": "pull requests"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "loaded 1 tool(s): GH") || !strings.Contains(res.Content, "found 1 skill(s): git-ops") ||
		!strings.Contains(res.Content, "Skill tool") {
		t.Fatalf("content = %q", res.Content)
	}
	if strings.Contains(res.Content, "SECRET") {
		t.Fatal("a description leaked into the result")
	}
	if len(cat.loaded) != 1 || cat.loaded[0] != "GH" {
		t.Fatalf("loaded = %v; a skill must not be loaded as a tool", cat.loaded)
	}
}

func TestToolSearchSelectsASkillByName(t *testing.T) {
	cat := newFull()
	res, _ := ToolSearch{Catalog: cat}.Execute(context.Background(), map[string]any{"query": "select:PDF-tools, jq"})
	if !strings.Contains(res.Content, "loaded 1 tool(s): JQ") || !strings.Contains(res.Content, "found 1 skill(s): pdf-tools") {
		t.Fatalf("content = %q", res.Content)
	}
	if cat.rankSeen != 0 {
		t.Fatal("select: went through the ranker")
	}
}

func TestToolSearchUsesTheRankerAndFallsBackWhenItDeclines(t *testing.T) {
	cat := newFull()
	cat.rank = func(q string, det, pool []Candidate, limit int) ([]Candidate, bool) {
		return []Candidate{{Name: "JQ"}}, true // the ranker replaces the list
	}
	res, _ := ToolSearch{Catalog: cat}.Execute(context.Background(), map[string]any{"query": "pull requests"})
	if !strings.Contains(res.Content, "loaded 1 tool(s): JQ") || strings.Contains(res.Content, "GH") {
		t.Fatalf("ranked content = %q", res.Content)
	}
	if rankedNames(cat.gotDet) != "GH,git-ops" || len(cat.gotPool) != 4 {
		t.Fatalf("ranker saw det=%s pool=%d", rankedNames(cat.gotDet), len(cat.gotPool))
	}

	cat = newFull() // rank == nil: ok=false
	res, _ = ToolSearch{Catalog: cat}.Execute(context.Background(), map[string]any{"query": "pull requests"})
	if !strings.Contains(res.Content, "GH") || cat.rankSeen != 1 {
		t.Fatalf("fallback content = %q (ranker calls %d)", res.Content, cat.rankSeen)
	}
}

func TestToolSearchNoMatchMessageMentionsSkillsWhenTheyExist(t *testing.T) {
	res, _ := ToolSearch{Catalog: newFull()}.Execute(context.Background(), map[string]any{"query": "quantum"})
	if !strings.Contains(res.Content, "no deferred tool or skill matches") || !strings.Contains(res.Content, "deferred tools: GH, JQ") {
		t.Fatalf("content = %q", res.Content)
	}
	only := &fakeCatalog{}
	res, _ = ToolSearch{Catalog: only}.Execute(context.Background(), map[string]any{"query": "quantum"})
	if !strings.Contains(res.Content, "no deferred tools remain") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestToolSearchRespectsMaxResults(t *testing.T) {
	cat := &fakeCatalog{}
	for _, n := range []string{"ToolA", "ToolB", "ToolC", "ToolD"} {
		cat.deferred = append(cat.deferred, Definition{Name: n, Description: "shared words here"})
	}
	res, _ := ToolSearch{Catalog: cat}.Execute(context.Background(), map[string]any{"query": "shared words", "max_results": float64(2)})
	if !strings.Contains(res.Content, "loaded 2 tool(s)") {
		t.Fatalf("content = %q", res.Content)
	}
}
