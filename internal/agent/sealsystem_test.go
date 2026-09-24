package agent

import (
	"testing"

	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/run"
)

func TestSealSystemReusesBytesUntilAnInputChanges(t *testing.T) {
	pool := nonce.New()
	if err := pool.Seed(16); err != nil {
		t.Fatal(err)
	}
	s := &Session{cfg: run.Config{Provider: "openai", Model: "gpt-5"}, pool: pool}
	opts := prompt.Options{RepoMap: "Repository map (harness-computed facts, not repository prose):\nroot: /r"}

	first, err := s.sealSystem(opts)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.sealSystem(opts)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("an unchanged prompt must reuse the sealed bytes (and nonces)")
	}

	opts.WorkDiscipline = true // a mode switch
	switched, err := s.sealSystem(opts)
	if err != nil {
		t.Fatal(err)
	}
	if switched == first {
		t.Fatal("a changed input must re-seal")
	}

	s.cfg.Model = "gpt-5-mini" // a model switch
	if other, _ := s.sealSystem(opts); other == switched {
		t.Fatal("a model switch must re-seal")
	}
}

func TestJoinDirectivesSkipsEmptyBodies(t *testing.T) {
	if got := joinDirectives("", "status", "  "); got != "status" {
		t.Fatalf("got %q", got)
	}
	if got := joinDirectives("a", "b"); got != "a\n\nb" {
		t.Fatalf("got %q", got)
	}
}
