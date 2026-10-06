//go:build !belai_sandbox

package main

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

// Only the Pix Sandbox build has a built-in MCP server.
func TestNoBuiltinMCPInDefaultBuild(t *testing.T) {
	if builtinMCPServers != nil || builtinMCP(config.Settings{}, t.TempDir()) != nil {
		t.Fatal("the default build must have no built-in MCP server")
	}
}
