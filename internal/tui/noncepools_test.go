package tui

import "testing"

// A rebuilt agent session keeps the nonce pool of the one before it, so the
// sealed system prompt it renders is byte-identical and stays cached at the
// provider; a session for another endpoint gets its own pool.
func TestRebuiltSessionKeepsNoncePool(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	first, err := buildAgentSession(a.sessionBuildParams())
	if err != nil {
		t.Fatal(err)
	}
	a.invalidateAgentSession()
	second, err := buildAgentSession(a.sessionBuildParams())
	if err != nil {
		t.Fatal(err)
	}
	if first.NoncePool() == nil || first.NoncePool() != second.NoncePool() {
		t.Fatal("a rebuilt session must reuse the App's nonce pool")
	}
	p := a.sessionBuildParams()
	p.cfg.BaseURL = "https://other.example/v1"
	if a.noncePools.get(p.cfg) != nil {
		t.Fatal("another endpoint must not share the pool")
	}
}
