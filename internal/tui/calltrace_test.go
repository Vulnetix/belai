package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/calltrace"
)

// TestToolContextCarriesSessionAndTool pins that a tool the TUI runs directly
// (inline !cmd, @file admission, the file picker) carries the current session
// id and the tool identity.
func TestToolContextCarriesSessionAndTool(t *testing.T) {
	a := New(Options{})
	a.sessionID = "sess-tui"
	env := map[string]string{}
	for _, kv := range calltrace.Env(a.toolContext(context.Background(), "Bash", "shell-1")) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env[calltrace.EnvSessionID] != "sess-tui" || env[calltrace.EnvTool] != "Bash" || env[calltrace.EnvToolCallID] != "shell-1" {
		t.Fatalf("env = %v", env)
	}
}

// TestPublishSessionIDNilManagers pins that publishing before either manager
// exists is a no-op rather than a nil dereference.
func TestPublishSessionIDNilManagers(t *testing.T) {
	a := New(Options{})
	a.bgManager = nil
	a.procManager = nil
	a.publishSessionID()
}
