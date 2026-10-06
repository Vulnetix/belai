//go:build belai_sandbox

package clefmcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/jsonrpc"
)

// Name is the server's name; its tools reach the model as mcp__clef__<tool>.
const Name = "clef"

// protocolVersion is the MCP revision this server answers with.
const protocolVersion = "2025-06-18"

// Server answers MCP requests from the decider behind decider.
type Server struct {
	decider func() (decisions.Decider, error)
}

// New returns a server that resolves its decision backend on each call, so a
// settings change or a late login takes effect without a restart. decider may
// return an error when no decision model is configured.
func New(decider func() (decisions.Decider, error)) *Server { return &Server{decider: decider} }

type toolDef struct {
	name, desc string
	schema     map[string]any
	run        func(*Server, context.Context, json.RawMessage) (any, error)
}

// Handler is the server as a jsonrpc handler: initialize, ping, tools/list and
// tools/call.
func (s *Server) Handler() jsonrpc.Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": Name, "version": "1"},
			}, nil
		case "notifications/initialized", "notifications/cancelled":
			return nil, nil
		case "ping":
			return map[string]any{}, nil
		case "tools/list":
			return map[string]any{"tools": listing()}, nil
		case "tools/call":
			return s.call(ctx, params)
		}
		return nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "clef does not support %s", method)
	}
}

func listing() []map[string]any {
	out := make([]map[string]any, len(catalog))
	for i, t := range catalog {
		out[i] = map[string]any{"name": t.name, "description": t.desc, "inputSchema": t.schema}
	}
	return out
}

// Tools names every tool, in listing order.
func Tools() []string {
	out := make([]string, len(catalog))
	for i, t := range catalog {
		out[i] = t.name
	}
	return out
}

func (s *Server) call(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bad tools/call params")
	}
	for _, t := range catalog {
		if t.name != p.Name {
			continue
		}
		res, err := t.run(s, ctx, p.Arguments)
		if err != nil {
			var te *toolError
			if !errors.As(err, &te) {
				te = &toolError{Code: "error", Msg: "the tool failed"}
			}
			return result(map[string]any{"error": te.Code, "message": te.Msg}, true), nil
		}
		return result(res, false), nil
	}
	return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown tool %q", p.Name)
}

// result is an MCP tool result holding one JSON text item.
func result(v any, isErr bool) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		b, isErr = []byte(`{"error":"error","message":"the result could not be encoded"}`), true
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(b)}}, "isError": isErr}
}
