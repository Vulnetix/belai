package main

import (
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/jsonrpc"
)

// builtinMCPServers returns the MCP servers compiled into this binary, by
// name. It is nil in every build but the Pix Sandbox one, which sets it from
// mcp_builtin_sandbox.go, so no other build has a built-in server (docs/mcp.md).
var builtinMCPServers func(settings config.Settings, workdir string) map[string]jsonrpc.Handler

// builtinMCP is the Builtins option for mcp.StartAsync.
func builtinMCP(settings config.Settings, workdir string) map[string]jsonrpc.Handler {
	if builtinMCPServers == nil {
		return nil
	}
	return builtinMCPServers(settings, workdir)
}
