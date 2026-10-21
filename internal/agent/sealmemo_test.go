package agent

import (
	"testing"

	"github.com/vulnetix/belai/internal/nonce"
	"github.com/vulnetix/belai/internal/prompt"
	"github.com/vulnetix/belai/internal/run"
)

// A rebuilt session that keeps the pool and the memo sends the same sealed
// prefix; one on a different pool seals afresh, because its nonces must come
// from the pool that verifies them.
func TestSealMemoSurvivesASessionRebuild(t *testing.T) {
	pool := nonce.New()
	if err := pool.Seed(16); err != nil {
		t.Fatal(err)
	}
	memo := &SealMemo{}
	cfg := run.Config{Provider: "openai", Model: "m"}
	opts := prompt.Options{}
	first, err := (&Session{cfg: cfg, pool: pool, seal: memo}).sealSystem(opts)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := (&Session{cfg: cfg, pool: pool, seal: memo}).sealSystem(opts)
	if again != first {
		t.Fatal("a rebuilt session re-sealed an unchanged prompt")
	}
	other := nonce.New()
	_ = other.Seed(16)
	if fresh, _ := (&Session{cfg: cfg, pool: other, seal: memo}).sealSystem(opts); fresh == first {
		t.Fatal("a prompt sealed with another pool's nonces was reused")
	}
}
