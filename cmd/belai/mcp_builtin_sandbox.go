//go:build belai_sandbox

package main

import (
	"errors"
	"sync"

	"github.com/vulnetix/belai/internal/clefmcp"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/run"
)

// The Pix Sandbox build carries one built-in MCP server, "clef", whose tools
// ask the classifier's decision model for a boolean, an enum pick, weights or
// an ordering (internal/clefmcp). It uses the same decision backend that
// answers guardrails, which in a sandbox is Clef on Workers AI through the
// sandbox Worker, so it adds no credential and no network destination.

func init() { builtinMCPServers = sandboxMCPServers }

func sandboxMCPServers(settings config.Settings, workdir string) map[string]jsonrpc.Handler {
	var (
		mu      sync.Mutex
		decider decisions.Decider
	)
	resolve := func() (decisions.Decider, error) {
		mu.Lock()
		defer mu.Unlock()
		if decider != nil {
			return decider, nil
		}
		var src run.CredentialSource
		if r, err := newResolver(workdir); err == nil && r != nil {
			src = r
		}
		cc, err := run.ResolveClassifier(run.Config{}, settings.Classifier, src)
		if err != nil {
			return nil, errors.New("the classifier is not set up for decisions")
		}
		if !cc.Decisions.On() || cc.Decisions.Backend != decisions.BackendSystemOne {
			return nil, errors.New("no Clef or SystemOne decision model is configured")
		}
		decider = cc.Decisions.NewDecider(httpclient.Default())
		return decider, nil
	}
	return map[string]jsonrpc.Handler{clefmcp.Name: clefmcp.New(resolve).Handler()}
}
