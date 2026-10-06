//go:build belai_sandbox

package main

import (
	"context"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/mcp"
)

// The sandbox build offers the clef server, with all nine tools, as KindMCP
// tools named mcp__clef__<tool>, and without any settings entry.
func TestSandboxBuildOffersClefMCP(t *testing.T) {
	m := mcp.Start(context.Background(), nil, mcp.Options{Builtins: builtinMCP(config.Settings{}, t.TempDir())})
	defer m.Close()
	st := m.Status()
	if len(st) != 1 || st[0].Name != "clef" || st[0].State != mcp.StateRunning || st[0].Transport != mcp.BuiltinTransport || len(st[0].Tools) != 9 {
		t.Fatalf("status = %+v", st)
	}
	for _, tl := range m.Tools() {
		if tl.Kind().ReadOnly() || !tl.Kind().NeedsClassifier() {
			t.Errorf("%s must be an MCP-kind tool (mutating, classified)", tl.Definition().Name)
		}
	}
}

// With no decision model configured, a call is a tool error, not a crash and
// not an answer.
func TestSandboxClefWithoutDecisionModel(t *testing.T) {
	m := mcp.Start(context.Background(), nil, mcp.Options{Builtins: builtinMCP(config.Settings{}, t.TempDir())})
	defer m.Close()
	for _, tl := range m.Tools() {
		if tl.Definition().Name != "mcp__clef__decide_boolean" {
			continue
		}
		res, err := tl.Execute(context.Background(), map[string]any{"question": "Is it?"})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Content; len(got) == 0 || got[:len("MCP tool reported an error")] != "MCP tool reported an error" {
			t.Errorf("content = %q", got)
		}
		return
	}
	t.Fatal("decide_boolean not offered")
}
