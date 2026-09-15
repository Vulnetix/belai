// Package modelinfo maps provider model ids to their context-window size.
package modelinfo

import (
	"strings"
)

// Info is the registry entry for one model id.
type Info struct {
	ID            string // canonical id, or the prefix that matched
	ContextWindow int
	MaxOutput     int // 0 when unknown; advisory only
}

type prefixEntry struct {
	prefix string
	info   Info
}

var exact = map[string]Info{}
var prefixes = []prefixEntry{}

func init() {
	register := func(window, maxOutput int, ids ...string) {
		for _, id := range ids {
			exact[id] = Info{ID: id, ContextWindow: window, MaxOutput: maxOutput}
		}
	}
	registerPrefix := func(prefix string, window, maxOutput int) {
		prefixes = append(prefixes, prefixEntry{prefix: prefix, info: Info{ID: prefix, ContextWindow: window, MaxOutput: maxOutput}})
	}

	register(1_000_000, 128_000,
		"claude-opus-5", "claude-sonnet-5",
		"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8",
		"claude-sonnet-4-6", "claude-fable-5", "claude-fable-5-1")
	register(1_000_000, 64_000, "claude-sonnet-4-5")
	register(200_000, 64_000, "claude-opus-4-5", "claude-haiku-4-5")

	register(400_000, 128_000, "gpt-5", "gpt-5-mini", "gpt-5-nano", "gpt-5-pro", "gpt-5.1", "gpt-5.2")
	register(272_000, 128_000, "gpt-5.4", "gpt-5.5")
	register(1_047_576, 32_768, "gpt-4.1", "gpt-4.1-mini")
	register(128_000, 16_384, "gpt-4o", "gpt-4o-mini", "gpt-4-turbo")
	register(200_000, 100_000, "o1", "o1-pro", "o3", "o3-mini", "o3-pro", "o4-mini")

	register(262_144, 256_000, "@cf/moonshotai/kimi-k2.6")
	register(128_000, 16_384, "@cf/openai/gpt-oss-120b", "@cf/openai/gpt-20b", "@cf/openai/gpt-oss-20b")
	register(131_000, 16_384, "@cf/meta/llama-4-scout-17b-16e-instruct")
	register(32_768, 32_768, "@cf/qwen/qwen3-30b-a3b-fp8")

	// New built-in adapters (OpenRouter, Gemini). Ollama's model list is
	// host-specific, so it is typed or imported and has no registry entry.
	register(1_000_000, 65_536, "gemini-2.5-flash", "gemini-2.5-pro")
	register(1_048_576, 8_192, "gemini-2.0-flash")
	register(128_000, 16_384, "openai/gpt-4o", "openai/gpt-4o-mini")
	register(200_000, 64_000, "anthropic/claude-3.5-sonnet", "anthropic/claude-3.7-sonnet")

	// Conservative prefixes, longest-first. Exact entries always win, so the
	// divergent in-family ids (claude-opus-4-5, gpt-5.4, gpt-5.5) still resolve
	// correctly before these fall through to a family bucket.
	registerPrefix("claude-opus-", 1_000_000, 128_000)
	registerPrefix("claude-sonnet-", 1_000_000, 64_000)
	registerPrefix("claude-haiku-", 200_000, 64_000)
	registerPrefix("gpt-5", 400_000, 128_000)
	registerPrefix("gpt-4.1", 1_047_576, 32_768)
	registerPrefix("gpt-4", 128_000, 16_384)
	registerPrefix("o1", 200_000, 100_000)
	registerPrefix("o3", 200_000, 100_000)
	registerPrefix("o4", 200_000, 100_000)

	// Sort prefixes longest-first.
	for i := 0; i < len(prefixes); i++ {
		for j := i + 1; j < len(prefixes); j++ {
			if len(prefixes[j].prefix) > len(prefixes[i].prefix) {
				prefixes[i], prefixes[j] = prefixes[j], prefixes[i]
			}
		}
	}
}

// Lookup resolves a model id. Matching is: exact (case-folded, trimmed, with a
// leading "workers-ai/" stripped), then exact again with "." folded to "-",
// then longest-prefix. An unlisted model returns ok=false; callers render
// "unknown" and never a guess — a wrong denominator is worse than none.
func Lookup(model string) (Info, bool) {
	n := normalize(model)
	if v, ok := exact[n]; ok {
		return v, true
	}
	d := foldDots(n)
	if v, ok := exact[d]; ok {
		return v, true
	}
	if v, ok := prefixMatch(n); ok {
		return v, true
	}
	if d != n {
		if v, ok := prefixMatch(d); ok {
			return v, true
		}
	}
	return Info{}, false
}

// Resolve applies a user override before falling back to the registry.
// overrides is config.Settings.ContextWindows; exact ids only, nil-safe.
func Resolve(model string, overrides map[string]int) (int, bool) {
	if v, ok := overrides[model]; ok {
		return v, true
	}
	if v, ok := overrides[normalize(model)]; ok {
		return v, true
	}
	info, ok := Lookup(model)
	if !ok {
		return 0, false
	}
	return info.ContextWindow, true
}

func normalize(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	s = strings.TrimPrefix(s, "workers-ai/")
	return s
}

func foldDots(model string) string {
	return strings.ReplaceAll(model, ".", "-")
}

func prefixMatch(model string) (Info, bool) {
	for _, p := range prefixes {
		if strings.HasPrefix(model, p.prefix) {
			return p.info, true
		}
	}
	return Info{}, false
}
