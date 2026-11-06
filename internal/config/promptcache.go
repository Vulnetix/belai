package config

import (
	"fmt"
	"time"
)

// Prompt-cache TTLs and keep-alive bounds (docs/prompt-cache.md).
const (
	// PromptCacheTTL5m is the provider's default cache lifetime and the
	// default of prompt_cache.ttl: nothing extra is sent for it.
	PromptCacheTTL5m = "5m"
	// PromptCacheTTL1h asks for the longer lifetime, at a higher write price.
	PromptCacheTTL1h = "1h"

	// MaxPromptCacheKeepalive caps prompt_cache.keepalive_max. A ping costs
	// the user's tokens, so the ceiling is a deliberate small number.
	MaxPromptCacheKeepalive = 100

	// MinPromptCacheEveryS is the shortest gap between pings.
	MinPromptCacheEveryS = 60
	// MaxPromptCacheEvery5mS keeps a ping inside a 5 minute entry's life.
	MaxPromptCacheEvery5mS = 290
	// MaxPromptCacheEvery1hS keeps a ping inside a 1 hour entry's life.
	MaxPromptCacheEvery1hS = 3500

	// DefaultPromptCacheEvery5mS and DefaultPromptCacheEvery1hS are the gaps
	// used when keepalive_every_s is unset.
	DefaultPromptCacheEvery5mS = 270
	DefaultPromptCacheEvery1hS = 3300
)

// PromptCacheTTLs are the values of prompt_cache.ttl.
var PromptCacheTTLs = []string{PromptCacheTTL5m, PromptCacheTTL1h}

// PromptCacheSettings configures how long a provider keeps the request prefix
// warm and whether the harness refreshes it while the session is idle
// (docs/prompt-cache.md). A per-user block: a cache write at the longer TTL
// and every keep-alive ping spend the user's tokens, so the project layer is
// dropped and a repository cannot change either.
type PromptCacheSettings struct {
	// TTL is the cache lifetime requested on every breakpoint: "5m" (the
	// default) or "1h". Only providers that honour cache_control with a ttl
	// receive it; the others keep their own lifetime.
	TTL string `json:"ttl,omitempty"`
	// KeepaliveMax is the most pings sent for one quiet stretch. Zero, the
	// default, is off.
	KeepaliveMax *int `json:"keepalive_max,omitempty"`
	// KeepaliveEveryS is the idle gap, in seconds, before a ping. Clamped to
	// the entry's lifetime (see PromptCacheKeepaliveEvery).
	KeepaliveEveryS *int `json:"keepalive_every_s,omitempty"`
}

// PromptCacheTTL returns the effective cache lifetime: "1h" only when asked
// for, "5m" otherwise (an unknown value falls back, never widens).
func (s Settings) PromptCacheTTL() string {
	if p := s.PromptCache; p != nil && p.TTL == PromptCacheTTL1h {
		return PromptCacheTTL1h
	}
	return PromptCacheTTL5m
}

// PromptCacheKeepaliveMax returns the most pings per quiet stretch, 0 (off)
// by default and clamped to [0, MaxPromptCacheKeepalive].
func (s Settings) PromptCacheKeepaliveMax() int {
	if p := s.PromptCache; p != nil && p.KeepaliveMax != nil {
		return min(max(*p.KeepaliveMax, 0), MaxPromptCacheKeepalive)
	}
	return 0
}

// PromptCacheKeepaliveEvery returns the idle gap before a ping. Unset means
// 270 s for a 5 minute cache and 3300 s for a 1 hour cache; a set value is
// clamped to 60 s .. 290 s or 3500 s so a ping always lands before the entry
// it refreshes has expired.
func (s Settings) PromptCacheKeepaliveEvery() time.Duration {
	hi, def := MaxPromptCacheEvery5mS, DefaultPromptCacheEvery5mS
	if s.PromptCacheTTL() == PromptCacheTTL1h {
		hi, def = MaxPromptCacheEvery1hS, DefaultPromptCacheEvery1hS
	}
	secs := def
	if p := s.PromptCache; p != nil && p.KeepaliveEveryS != nil {
		secs = *p.KeepaliveEveryS
	}
	return time.Duration(min(max(secs, MinPromptCacheEveryS), hi)) * time.Second
}

func (p *PromptCacheSettings) merge(from *PromptCacheSettings) {
	if from.TTL != "" {
		p.TTL = from.TTL
	}
	if from.KeepaliveMax != nil {
		p.KeepaliveMax = from.KeepaliveMax
	}
	if from.KeepaliveEveryS != nil {
		p.KeepaliveEveryS = from.KeepaliveEveryS
	}
}

// ValidatePromptCache rejects a prompt_cache block with an unknown ttl or a
// number outside its bounds, naming the key.
func ValidatePromptCache(s Settings) error {
	p := s.PromptCache
	if p == nil {
		return nil
	}
	if p.TTL != "" && p.TTL != PromptCacheTTL5m && p.TTL != PromptCacheTTL1h {
		return fmt.Errorf("prompt_cache.ttl %q must be %s or %s", p.TTL, PromptCacheTTL5m, PromptCacheTTL1h)
	}
	if p.KeepaliveMax != nil && (*p.KeepaliveMax < 0 || *p.KeepaliveMax > MaxPromptCacheKeepalive) {
		return fmt.Errorf("prompt_cache.keepalive_max %d must be between 0 and %d", *p.KeepaliveMax, MaxPromptCacheKeepalive)
	}
	if p.KeepaliveEveryS != nil && (*p.KeepaliveEveryS < MinPromptCacheEveryS || *p.KeepaliveEveryS > MaxPromptCacheEvery1hS) {
		return fmt.Errorf("prompt_cache.keepalive_every_s %d must be between %d and %d", *p.KeepaliveEveryS, MinPromptCacheEveryS, MaxPromptCacheEvery1hS)
	}
	return nil
}
