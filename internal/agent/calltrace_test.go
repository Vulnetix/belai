package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/tools"
)

// traceProbe is a tool that records the calltrace environment its Execute
// context carries.
type traceProbe struct{ env map[string]string }

func (p *traceProbe) Definition() tools.Definition  { return tools.Definition{Name: "Probe"} }
func (p *traceProbe) Kind() tools.Kind              { return tools.KindRead }
func (p *traceProbe) Subject(map[string]any) string { return "" }
func (p *traceProbe) Execute(ctx context.Context, _ map[string]any) (tools.Result, error) {
	p.env = map[string]string{}
	for _, kv := range calltrace.Env(ctx) {
		k, v, _ := strings.Cut(kv, "=")
		p.env[k] = v
	}
	return tools.Result{}, nil
}

// TestRunToolStampsToolIdentity pins that runTool carries the session from
// the turn context and adds the tool's registered name (not the model's
// spelling) and the model's call id.
func TestRunToolStampsToolIdentity(t *testing.T) {
	probe := &traceProbe{}
	ctx := calltrace.WithSession(context.Background(), "sess-run")
	call := rolemanager.ToolCall{ID: "call_42", Name: "probe"} // lower-cased by the model
	if _, err := runTool(ctx, probe, call, func(Event) {}); err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if probe.env[calltrace.EnvSessionID] != "sess-run" {
		t.Errorf("session = %q", probe.env[calltrace.EnvSessionID])
	}
	if probe.env[calltrace.EnvTool] != "Probe" {
		t.Errorf("tool = %q, want the registered spelling Probe", probe.env[calltrace.EnvTool])
	}
	if probe.env[calltrace.EnvToolCallID] != "call_42" {
		t.Errorf("call id = %q", probe.env[calltrace.EnvToolCallID])
	}
	if probe.env[calltrace.EnvTraceparent] == "" {
		t.Error("TRACEPARENT missing inside a session")
	}
}

// TestRunToolWithoutSession pins the no-session edge: the tool identity is
// still stamped, but there is no session id and no traceparent.
func TestRunToolWithoutSession(t *testing.T) {
	probe := &traceProbe{}
	if _, err := runTool(context.Background(), probe, rolemanager.ToolCall{ID: "c"}, func(Event) {}); err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if _, ok := probe.env[calltrace.EnvSessionID]; ok {
		t.Error("session id set without a session")
	}
	if _, ok := probe.env[calltrace.EnvTraceparent]; ok {
		t.Error("TRACEPARENT set without a session")
	}
	if probe.env[calltrace.EnvTool] != "Probe" {
		t.Errorf("tool = %q", probe.env[calltrace.EnvTool])
	}
}
