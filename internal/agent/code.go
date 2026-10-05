package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/codemode"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/tools"
)

// Code mode (docs/code-mode.md). A model-written script calls tools as
// functions. Every nested call goes through executeCall, the same pipeline a
// direct call takes, so permissions, hooks, the ask gate, the sandbox, the
// file-diff recorder, sanitising and the classifier all apply; the script
// only ever sees text that pipeline admitted, or its withheld message.

// maxNestedResultEvent caps the nested result text sent to the render-only
// event stream. The script still gets the whole admitted result.
const maxNestedResultEvent = 2048

type codeCallKey struct{}

// codeCall is what the outer Code call hands its nested calls: where to emit,
// where to fold their observed file effects, and the id they derive theirs from.
type codeCall struct {
	emit     func(Event)
	eff      *callEffect
	parentID string
	seq      atomic.Int64
	mu       sync.Mutex
}

func withCodeCall(ctx context.Context, cc *codeCall) context.Context {
	return context.WithValue(ctx, codeCallKey{}, cc)
}

func codeCallFrom(ctx context.Context) *codeCall {
	cc, _ := ctx.Value(codeCallKey{}).(*codeCall)
	return cc
}

// resolveCall resolves a call to a tool. A nested call resolves against what a
// script may reach; every other call resolves as execTool always did.
func (s *Session) resolveCall(ctx context.Context, name string) (tools.Tool, string) {
	if !tools.IsNested(ctx) {
		return s.execTool(name)
	}
	if !s.turnCode {
		return nil, fmt.Sprintf("tool result withheld: %q is not available from a script outside code mode", name)
	}
	t, ok := s.registry.Find(name)
	if !ok {
		return nil, fmt.Sprintf("tool result withheld: %q is not registered", name)
	}
	if t.Kind() != tools.KindMCP && !tools.NestedAllowed(t) {
		return nil, fmt.Sprintf("tool result withheld: %q cannot be called from a script", name)
	}
	return t, ""
}

// runCode is the Code tool's runner.
func (s *Session) runCode(ctx context.Context, script string) (string, error) {
	cc := codeCallFrom(ctx)
	if cc == nil {
		return "", fmt.Errorf("code mode is not available here")
	}
	builtin, mcpTools := s.registry.NestedTools()

	names := make([]string, 0, len(builtin))
	byName := map[string]tools.Tool{}
	for _, t := range builtin {
		n := t.Definition().Name
		names = append(names, n)
		byName[n] = t
	}
	sort.Strings(names)

	mcpNames := map[string][]string{}
	mcpByKey := map[string]tools.Tool{}
	mcpFull := map[string]string{}
	for _, t := range mcpTools {
		server, tool, ok := splitMCPName(t.Definition().Name)
		if !ok {
			continue
		}
		mcpNames[server] = append(mcpNames[server], tool)
		mcpByKey[server+"."+tool] = t
		mcpFull[server+"."+tool] = t.Definition().Name
	}
	for _, v := range mcpNames {
		sort.Strings(v)
	}

	call := func(ctx context.Context, name string, args map[string]any) string {
		return s.nestedCall(ctx, cc, name, args)
	}
	env := codemode.Env{
		Call:  call,
		Tools: names,
		ReadOnly: func(name string) bool {
			t, ok := byName[name]
			return ok && !tools.Mutates(t)
		},
		Search: func(q string) []string {
			q = strings.ToLower(strings.TrimSpace(q))
			var out []string
			for _, n := range names {
				if q == "" || strings.Contains(strings.ToLower(n), q) {
					out = append(out, "tools."+n)
				}
			}
			keys := make([]string, 0, len(mcpByKey))
			for k := range mcpByKey {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if q == "" || strings.Contains(strings.ToLower(k), q) {
					out = append(out, "mcp."+k)
				}
			}
			if len(out) > 25 {
				out = out[:25]
			}
			return out
		},
		Describe: func(name string) string {
			t, ok := byName[name]
			if !ok {
				return ""
			}
			return describeDefinition(t.Definition())
		},
		MCP:     mcpNames,
		MCPName: func(server, tool string) string { return mcpFull[server+"."+tool] },
		MCPDescribe: func(ctx context.Context, server, tool string) string {
			t, ok := mcpByKey[server+"."+tool]
			if !ok {
				return ""
			}
			// The definition is server-written text. It reaches the model only
			// through the sealed briefing elsewhere; here it is admitted the way
			// an MCP result is, so a script or the model never reads it raw.
			id := fmt.Sprintf("%s.d%d", cc.parentID, cc.seq.Add(1))
			pseudo := rolemanager.ToolCall{ID: id, Name: t.Definition().Name}
			return s.promoteResult(tools.WithNested(ctx), pseudo, tools.Result{Kind: tools.KindMCP, Content: describeDefinition(t.Definition())}, cc.emit)
		},
	}

	timeoutMS, maxCalls, maxOut := s.settings.CodeLimits()
	res := codemode.Run(ctx, script, env, codemode.Config{
		Timeout:   time.Duration(timeoutMS) * time.Millisecond,
		MaxCalls:  maxCalls,
		MaxOutput: maxOut,
	})
	return res.Output, res.Err
}

// nestedCall runs one script call through the session's own pipeline.
func (s *Session) nestedCall(ctx context.Context, cc *codeCall, name string, args map[string]any) string {
	call := rolemanager.ToolCall{ID: fmt.Sprintf("%s.%d", cc.parentID, cc.seq.Add(1)), Name: name, Args: args}
	emit := cc.emit
	if emit == nil {
		emit = func(Event) {}
	}
	started := call
	emit(Event{Kind: EventToolStartKind, Tool: &started})
	begin := time.Now()
	var eff callEffect
	out := s.executeCall(tools.WithNested(ctx), call, emit, &eff)
	shown := out
	if len(shown) > maxNestedResultEvent {
		shown = cutRunes(shown, maxNestedResultEvent) + "…"
	}
	emit(Event{Kind: EventToolResultKind, ToolName: name, ToolCallID: call.ID, ToolResult: shown, Duration: time.Since(begin)})
	if cc.eff != nil && (eff.changed || len(eff.paths) > 0) {
		cc.mu.Lock()
		cc.eff.changed = cc.eff.changed || eff.changed
		cc.eff.paths = append(cc.eff.paths, eff.paths...)
		cc.mu.Unlock()
	}
	return out
}

func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// splitMCPName splits mcp__server__tool. Server names hold no double
// underscore (letters, digits, _ and -), so the first "__" after the prefix
// ends the server name.
func splitMCPName(full string) (server, tool string, ok bool) {
	rest, found := strings.CutPrefix(full, "mcp__")
	if !found {
		return "", "", false
	}
	server, tool, ok = strings.Cut(rest, "__")
	return server, tool, ok && server != "" && tool != ""
}

// describeDefinition renders a tool's name, text and argument shape.
func describeDefinition(d tools.Definition) string {
	var b strings.Builder
	b.WriteString(d.Name)
	if d.Description != "" {
		b.WriteString(": ")
		b.WriteString(d.Description)
	}
	keys := make([]string, 0, len(d.Properties))
	for k := range d.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	req := map[string]bool{}
	for _, r := range d.Required {
		req[r] = true
	}
	for _, k := range keys {
		p := d.Properties[k]
		b.WriteString("\n- " + k + " (" + p.Type)
		if req[k] {
			b.WriteString(", required")
		}
		b.WriteString(")")
		if p.Description != "" {
			b.WriteString(": " + p.Description)
		}
	}
	return b.String()
}
