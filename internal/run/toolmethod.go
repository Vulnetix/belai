package run

import (
	"fmt"

	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/wire"
)

// ToolMethod is the wire method for serialising tool-call arguments. It is an
// alias of wire.ToolMethod so callers do not have to import the wire package
// for this concern.
type ToolMethod = wire.ToolMethod

const (
	ToolMethodNone   = wire.ToolMethodNone
	ToolMethodString = wire.ToolMethodString
	ToolMethodObject = wire.ToolMethodObject
	ToolMethodBlocks = wire.ToolMethodBlocks
)

// DetectToolMethod derives the tool calling method for cfg without network
// I/O, from the provider name, wire surface, and model identifier. An
// explicit cfg.ToolMethod always wins: it is the session-stored result of an
// earlier detection (or a provider's empirical correction) and re-deriving
// would discard the correction.
func DetectToolMethod(cfg Config) (ToolMethod, error) {
	if cfg.ToolMethod != ToolMethodNone {
		return cfg.ToolMethod, nil
	}
	if cfg.API != "" {
		switch cfg.API {
		case wire.SurfaceAnthropicMessages:
			return ToolMethodBlocks, nil
		case wire.SurfaceOpenAIChat:
			return ToolMethodString, nil
		}
		return ToolMethodNone, fmt.Errorf("provider surface %q has no known tool method", cfg.API)
	}
	d, ok := provider.Lookup(cfg.Provider)
	if !ok {
		return ToolMethodNone, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
	// Cloudflare AI Gateway's surface depends on the upstream model, so its
	// tool method is resolved here instead of in the descriptor.
	if cfg.Provider == "cloudflare-ai-gateway" {
		if isClaudeModel(cfg.Model) {
			return ToolMethodBlocks, nil
		}
		return ToolMethodString, nil
	}
	return d.ToolMethod, nil
}
