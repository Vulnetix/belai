package modeltest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
)

// TestLiveLocalDecisionLadder runs the whole local ladder against the real
// Hugging Face download and the llama-server on PATH. It downloads ~2.6 GB,
// so it runs only with BELAI_LIVE_DECISION set to a catalogue id
// (decider-4b or plumb-4b), and BELAI_MODELS_DIR pointing somewhere roomy.
func TestLiveLocalDecisionLadder(t *testing.T) {
	id := os.Getenv("BELAI_LIVE_DECISION")
	if id == "" {
		t.Skip("set BELAI_LIVE_DECISION=decider-4b|plumb-4b to run")
	}
	m, ok := decisions.LocalModelByID(id)
	if !ok {
		t.Fatalf("unknown model %s", id)
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	var lastPct int64 = -1
	rep := Run(ctx, DecisionLocalSteps(m, 0, 0), &Env{
		NGL: -1,
		Confirm: func(_ context.Context, o DownloadOffer) bool {
			t.Logf("confirm: %s (%s)", o.What, Sizes(o.Size))
			return true
		},
	}, func(e Event) {
		switch e.Kind {
		case EventDone:
			t.Logf("%-12s %-5s %s (%s)", e.Name, e.Outcome.Status, e.Outcome.Detail, e.Elapsed.Round(time.Millisecond))
			for _, h := range e.Outcome.Hints {
				t.Logf("    hint [%s] %s", h.Key, h.Text)
			}
		case EventProgress:
			if e.Total > 0 {
				if pct := e.Done * 100 / e.Total; pct/10 != lastPct/10 {
					lastPct = pct
					t.Logf("download %d%%", pct)
				}
			}
		}
	})
	defer rep.Release()
	if !rep.Passed {
		f, _ := rep.Failure()
		t.Fatalf("ladder failed at %s: %s", f.Name, f.Outcome.Detail)
	}
}
