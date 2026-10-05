package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/tools"
)

func builtinOpts() Options {
	return Options{Workdir: "", Builtins: map[string]jsonrpc.Handler{"fake": fakeHandler}}
}

func TestBuiltinServerRunsInProcess(t *testing.T) {
	m := Start(context.Background(), nil, builtinOpts())
	defer m.Close()
	checkEcho(t, m)
	if st := m.Status(); st[0].Transport != BuiltinTransport {
		t.Errorf("transport = %q, want %q", st[0].Transport, BuiltinTransport)
	}
}

func TestBuiltinToolsStayMCPKind(t *testing.T) {
	m := Start(context.Background(), nil, builtinOpts())
	defer m.Close()
	for _, tl := range m.Tools() {
		if tl.Kind() != tools.KindMCP || !strings.HasPrefix(tl.Definition().Name, "mcp__fake__") {
			t.Errorf("%s: kind %v", tl.Definition().Name, tl.Kind())
		}
	}
}

// A settings entry of a built-in's name cannot replace it, and nothing in the
// session can remove it.
func TestBuiltinCannotBeShadowedOrRemoved(t *testing.T) {
	cfg := &config.MCPSettings{Servers: map[string]config.MCPServer{
		"fake": {Command: "/bin/false"},
	}}
	m := Start(context.Background(), cfg, builtinOpts())
	defer m.Close()
	checkEcho(t, m)
	if err := m.Upsert(context.Background(), "fake", config.MCPServer{Command: "/bin/false"}); err == nil {
		t.Error("Upsert must refuse a built-in name")
	}
	m.Remove("fake")
	if len(m.Status()) != 1 {
		t.Error("Remove must leave a built-in in place")
	}
	if err := m.Restart(context.Background(), "fake"); err != nil {
		t.Errorf("Restart: %v", err)
	}
	checkEcho(t, m)
}

func TestNoBuiltinsByDefault(t *testing.T) {
	m := Start(context.Background(), nil, Options{})
	defer m.Close()
	if len(m.Status()) != 0 || len(m.Tools()) != 0 {
		t.Error("a manager without Builtins must start no server")
	}
}
