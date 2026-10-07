package tools

import (
	"context"
	"slices"
	"testing"
)

// probeTool is a tool of a fixed name and kind.
type probeTool struct {
	name string
	kind Kind
}

func (p probeTool) Definition() Definition      { return Definition{Name: p.name, Description: p.name} }
func (p probeTool) Kind() Kind                  { return p.kind }
func (probeTool) Subject(map[string]any) string { return "" }
func (p probeTool) Execute(context.Context, map[string]any) (Result, error) {
	return Result{Kind: p.kind, Content: "ok"}, nil
}

// A decision tool only reads, so it stays wherever a read-only surface does: plan
// mode, read_only agent turns and code mode. An ordinary MCP tool is mutating
// and is dropped from the first two and kept out of code mode's advertised set.
func TestDecisionToolsSurviveEveryReadOnlySurface(t *testing.T) {
	decision := probeTool{"mcp__clef__decide_boolean", KindDecision}
	other := probeTool{"mcp__other__do", KindMCP}
	reg := NewRegistry(decision, other)

	has := func(r *Registry, name string) bool { return slices.Contains(r.Names(), name) }

	for name, r := range map[string]*Registry{
		"plan":                 reg.Plan(),
		"read_only":            reg.ReadOnlySurface(),
		"read_only (ReadOnly)": reg.ReadOnly(),
	} {
		if !has(r, decision.name) {
			t.Errorf("%s surface must keep the decision tool", name)
		}
		if has(r, other.name) {
			t.Errorf("%s surface must drop the mutating MCP tool", name)
		}
	}

	code := reg.CodeSurface(&Code{})
	if !has(code, decision.name) {
		t.Error("code mode must advertise the decision tool directly")
	}
	if has(code, other.name) {
		t.Error("code mode must not advertise an ordinary MCP tool")
	}

	builtin, mcp := reg.NestedTools()
	var inBuiltin, inMCP []string
	for _, b := range builtin {
		inBuiltin = append(inBuiltin, b.Definition().Name)
	}
	for _, m := range mcp {
		inMCP = append(inMCP, m.Definition().Name)
	}
	if !slices.Contains(inBuiltin, decision.name) {
		t.Errorf("a script must reach the decision tool as tools.<name>: builtin=%v", inBuiltin)
	}
	if !slices.Equal(inMCP, []string{other.name}) {
		t.Errorf("only the ordinary MCP tool is in the mcp namespace: %v", inMCP)
	}
	if Mutates(decision) || !Mutates(other) {
		t.Error("the decision tool does not mutate; the ordinary MCP tool does")
	}
	if decision.Kind().NeedsClassifier() || !other.Kind().NeedsClassifier() {
		t.Error("the decision kind is sanitise-only; the MCP kind is classified")
	}
}
