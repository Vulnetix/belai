package agent

import (
	"testing"

	"github.com/vulnetix/belai/internal/run"
)

// The classifier pipeline is built once per config, not once per turn or per
// tool result, and again only after the config changes.
func TestSessionPipelineIsBuiltOncePerConfig(t *testing.T) {
	s := &Session{cfg: run.Config{Provider: "openai", Model: "a"}}
	emit := func(Event) {}
	first := s.pipeline(emit)
	if s.pipeline(emit) != first || s.pipe.builds.Load() != 1 {
		t.Fatalf("rebuilt with an unchanged config (%d builds)", s.pipe.builds.Load())
	}
	s.cfg.Model = "b"
	if s.pipeline(emit) == first || s.pipe.builds.Load() != 2 {
		t.Fatal("a config change kept the old pipeline")
	}
}
