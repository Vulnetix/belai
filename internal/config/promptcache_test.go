package config

import (
	"strings"
	"testing"
	"time"
)

func TestPromptCacheDefaults(t *testing.T) {
	s := Settings{}
	if s.PromptCacheTTL() != "5m" || s.PromptCacheKeepaliveMax() != 0 || s.PromptCacheKeepaliveEvery() != 270*time.Second {
		t.Fatalf("defaults = %q %d %v", s.PromptCacheTTL(), s.PromptCacheKeepaliveMax(), s.PromptCacheKeepaliveEvery())
	}
	s = Settings{PromptCache: &PromptCacheSettings{TTL: "1h"}}
	if s.PromptCacheKeepaliveEvery() != 3300*time.Second {
		t.Fatalf("1h default gap = %v", s.PromptCacheKeepaliveEvery())
	}
}

func TestPromptCacheClampsAndFallsBack(t *testing.T) {
	cases := []struct {
		name string
		s    PromptCacheSettings
		want time.Duration
	}{
		{"too short", PromptCacheSettings{KeepaliveEveryS: ip(1)}, 60 * time.Second},
		{"too long for 5m", PromptCacheSettings{KeepaliveEveryS: ip(3000)}, 290 * time.Second},
		{"long is fine for 1h", PromptCacheSettings{TTL: "1h", KeepaliveEveryS: ip(3000)}, 3000 * time.Second},
		{"too long for 1h", PromptCacheSettings{TTL: "1h", KeepaliveEveryS: ip(9999)}, 3500 * time.Second},
	}
	for _, c := range cases {
		s := c.s
		if got := (Settings{PromptCache: &s}).PromptCacheKeepaliveEvery(); got != c.want {
			t.Errorf("%s: gap = %v, want %v", c.name, got, c.want)
		}
	}
	// An unknown ttl never widens to 1h, a negative count is off.
	bad := Settings{PromptCache: &PromptCacheSettings{TTL: "24h", KeepaliveMax: ip(-4)}}
	if bad.PromptCacheTTL() != "5m" || bad.PromptCacheKeepaliveMax() != 0 {
		t.Fatalf("fallback = %q %d", bad.PromptCacheTTL(), bad.PromptCacheKeepaliveMax())
	}
	if (Settings{PromptCache: &PromptCacheSettings{KeepaliveMax: ip(5000)}}).PromptCacheKeepaliveMax() != MaxPromptCacheKeepalive {
		t.Fatal("keepalive_max must be capped")
	}
}

func TestValidatePromptCacheNamesTheKey(t *testing.T) {
	for key, s := range map[string]Settings{
		"prompt_cache.ttl":               {PromptCache: &PromptCacheSettings{TTL: "2h"}},
		"prompt_cache.keepalive_max":     {PromptCache: &PromptCacheSettings{KeepaliveMax: ip(MaxPromptCacheKeepalive + 1)}},
		"prompt_cache.keepalive_every_s": {PromptCache: &PromptCacheSettings{KeepaliveEveryS: ip(10)}},
	} {
		if err := ValidatePromptCache(s); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	ok := Settings{PromptCache: &PromptCacheSettings{TTL: "1h", KeepaliveMax: ip(0), KeepaliveEveryS: ip(MaxPromptCacheEvery1hS)}}
	if err := ValidatePromptCache(ok); err != nil || ValidatePromptCache(Settings{}) != nil {
		t.Errorf("valid block refused: %v", err)
	}
}

// TestPromptCacheIsPerUser pins the layering rule in docs/prompt-cache.md: a
// repository cannot ask for the longer write or for pings.
func TestPromptCacheIsPerUser(t *testing.T) {
	wd := writeGlobal(t, `{"prompt_cache":{"keepalive_max":2}}`)
	writeProject(t, wd, `{"prompt_cache":{"ttl":"1h","keepalive_max":50,"keepalive_every_s":60}}`)
	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	s := eff.Settings
	if s.PromptCacheTTL() != "5m" || s.PromptCacheKeepaliveMax() != 2 || s.PromptCacheKeepaliveEvery() != 270*time.Second {
		t.Fatalf("the project layer changed the cache: %q %d %v", s.PromptCacheTTL(), s.PromptCacheKeepaliveMax(), s.PromptCacheKeepaliveEvery())
	}
	if eff.Origin["prompt_cache"] == SourceProject {
		t.Fatal("prompt_cache origin must not be the project layer")
	}
	var noted bool
	for _, n := range eff.Notes {
		noted = noted || strings.Contains(n, "prompt_cache")
	}
	if !noted {
		t.Errorf("no note says the project block was ignored: %v", eff.Notes)
	}
	merged := Settings{}.Override(Settings{PromptCache: &PromptCacheSettings{TTL: "1h", KeepaliveMax: ip(9)}})
	if merged.PromptCacheTTL() != "5m" || merged.PromptCacheKeepaliveMax() != 0 {
		t.Fatalf("Override let the project change the cache: %+v", merged.PromptCache)
	}
}

func TestPromptCacheGlobalOutOfRangeFailsResolve(t *testing.T) {
	wd := writeGlobal(t, `{"prompt_cache":{"ttl":"2h"}}`)
	if _, err := Resolve(wd, noEnv, Settings{}); err == nil || !strings.Contains(err.Error(), "prompt_cache.ttl") {
		t.Fatalf("err = %v", err)
	}
}
