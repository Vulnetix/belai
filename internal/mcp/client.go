// Package mcp is a Model Context Protocol client. It connects to the MCP
// servers named in the user's global settings, over stdio or streamable
// HTTP, lists their tools and offers each one to the model as
// mcp__<server>__<tool>. Every result is third-party text: the tools carry
// tools.KindMCP, which always classifies, and a server's names and
// descriptions are sanitized and capped before the model sees them.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/vulnetix/belai/internal/version"
)

// ProtocolVersion is the MCP revision Belai speaks.
const ProtocolVersion = "2025-06-18"

// transport carries JSON-RPC to one server.
type transport interface {
	call(ctx context.Context, method string, params, result any) error
	notify(ctx context.Context, method string, params any) error
	close() error
	// diag returns recent server diagnostics (stderr tail) for /mcp.
	diag() string
}

// ToolInfo is one tool a server offers.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// Content is one item of a tool result.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Resource *struct {
		URI  string `json:"uri,omitempty"`
		Text string `json:"text,omitempty"`
	} `json:"resource,omitempty"`
}

// CallResult is a tools/call result.
type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Client is a connected server.
type Client struct {
	name string
	t    transport
}

// initialize performs the MCP handshake.
func (c *Client) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "belai", "version": version.Version},
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.t.call(ctx, "initialize", params, &res); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return c.t.notify(ctx, "notifications/initialized", nil)
}

// listTools pages through tools/list.
func (c *Client) listTools(ctx context.Context) ([]ToolInfo, error) {
	var out []ToolInfo
	cursor := ""
	for page := 0; page < 50; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Tools      []ToolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := c.t.call(ctx, "tools/list", params, &res); err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		out = append(out, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// CallTool runs one server tool.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	var res CallResult
	err := c.t.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &res)
	return res, err
}

// Close ends the connection.
func (c *Client) Close() error { return c.t.close() }

// expand resolves an "env:NAME" value from Belai's environment.
func expand(v string) string {
	if name, ok := strings.CutPrefix(v, "env:"); ok {
		return os.Getenv(name)
	}
	return v
}
