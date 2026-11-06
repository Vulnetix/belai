package agent

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func cacheSession(t *testing.T, st config.Settings, ka *run.KeepAlive) *Session {
	t.Helper()
	root := t.TempDir()
	s, err := NewSession(Options{
		Cfg:       run.Config{Provider: "anthropic", BaseURL: "http://localhost", APIKey: "test", Model: "claude-sonnet-5"},
		Registry:  tools.Default(root, false),
		Posture:   posture.Defaults(),
		Workdir:   root,
		Settings:  st,
		KeepAlive: ka,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The cache lifetime follows the user's setting, and only a caller that owns a
// keep-alive gives the session one: a subagent, a worker or a headless run
// builds its options without it and so can never ping.
func TestSessionCacheTTLAndKeepAliveOwnership(t *testing.T) {
	plain := cacheSession(t, config.Settings{}, nil)
	if plain.cfg.CacheTTL != "" || plain.cfg.KeepAlive != nil {
		t.Fatalf("default session: ttl %q keepalive %v", plain.cfg.CacheTTL, plain.cfg.KeepAlive)
	}
	long := config.Settings{PromptCache: &config.PromptCacheSettings{TTL: "1h"}}
	ka := run.NewKeepAlive(nil)
	t.Cleanup(ka.Close)
	s := cacheSession(t, long, ka)
	if s.cfg.CacheTTL != "1h" || s.cfg.KeepAlive != ka {
		t.Fatalf("interactive session: ttl %q keepalive %v", s.cfg.CacheTTL, s.cfg.KeepAlive)
	}
	// A keep-alive on the incoming config is not inherited.
	root := t.TempDir()
	inherited, err := NewSession(Options{
		Cfg:      run.Config{Provider: "anthropic", BaseURL: "http://localhost", APIKey: "test", Model: "m", KeepAlive: ka},
		Registry: tools.Default(root, false), Posture: posture.Defaults(), Workdir: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inherited.cfg.KeepAlive != nil {
		t.Fatal("a session that was not given a keep-alive inherited one from its config")
	}
}
