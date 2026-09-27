package tools

import (
	"context"
	"strings"
	"testing"
)

func defs(names ...string) []Definition {
	out := make([]Definition, len(names))
	for i, n := range names {
		out[i] = Definition{Name: n, Description: "does " + strings.ToLower(n) + " things"}
	}
	return out
}

func names(ds []Definition) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return strings.Join(out, ",")
}

func TestDeferSetKeepsCoreAndAppendsLoaded(t *testing.T) {
	all := defs("Read", "JQ", "Write", "GH", "ToolSearch", "mcp__x__y")
	adv, deferred := DeferSet(all, nil)
	if names(adv) != "Read,Write,ToolSearch" || names(deferred) != "JQ,GH,mcp__x__y" {
		t.Fatalf("adv=%s deferred=%s", names(adv), names(deferred))
	}
	// Loaded tools follow the core ones in load order, so the cached prefix
	// of the tool list is unchanged by a load.
	adv, deferred = DeferSet(all, []string{"mcp__x__y", "jq"})
	if names(adv) != "Read,Write,ToolSearch,mcp__x__y,JQ" || names(deferred) != "GH" {
		t.Fatalf("after load adv=%s deferred=%s", names(adv), names(deferred))
	}
}

type fakeCatalog struct {
	deferred []Definition
	loaded   []string
}

func (f *fakeCatalog) Deferred() []Definition { return f.deferred }
func (f *fakeCatalog) Load(ns []string) []string {
	f.loaded = append(f.loaded, ns...)
	return ns
}

func TestToolSearchSelectKeywordsAndNamesOnly(t *testing.T) {
	cat := &fakeCatalog{deferred: []Definition{
		{Name: "GH", Description: "Query GitHub (read-only): pull requests and issues."},
		{Name: "JQ", Description: "Transform JSON using a jq filter."},
		{Name: "mcp__vulnetix__advisories", Description: "SECRET-SERVER-TEXT vendor advisories"},
	}}
	ts := ToolSearch{Catalog: cat}
	ctx := context.Background()

	res, err := ts.Execute(ctx, map[string]any{"query": "select:gh, JQ"})
	if err != nil || !strings.Contains(res.Content, "loaded 2 tool(s): GH, JQ") || res.Kind != KindToolSearch {
		t.Fatalf("select: %+v %v", res, err)
	}
	res, _ = ts.Execute(ctx, map[string]any{"query": "vendor advisories", "max_results": 1})
	if !strings.Contains(res.Content, "mcp__vulnetix__advisories") {
		t.Fatalf("keyword: %s", res.Content)
	}
	if strings.Contains(res.Content, "SECRET-SERVER-TEXT") {
		t.Fatal("ToolSearch leaked a tool description")
	}
	res, _ = ts.Execute(ctx, map[string]any{"query": "select:Nope"})
	if !strings.Contains(res.Content, "no deferred tool matches") || !strings.Contains(res.Content, "GH, JQ, mcp__vulnetix__advisories") {
		t.Fatalf("no match: %s", res.Content)
	}
	if _, err := ts.Execute(ctx, map[string]any{}); err == nil {
		t.Fatal("empty query accepted")
	}
	if Mutates(ts) || KindToolSearch.NeedsClassifier() || !KindToolSearch.ReadOnly() {
		t.Fatal("ToolSearch must be read-only and sanitise-only")
	}
}
