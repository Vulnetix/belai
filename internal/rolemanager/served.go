package rolemanager

import (
	"context"
	"sync"
)

// servedKey keys the served-model slot on a classifier call's context.
type servedKey struct{}

// servedSlot holds the provider/model that answered a classifier call. A
// tiered or routed classifier delegates to one leaf classifier, and only the
// leaf notes itself, so the slot names the model that actually replied — the
// fast tier, a Jev-routed winner, or the fallback — never the router.
type servedSlot struct {
	mu    sync.Mutex
	model string
}

// TrackServedModel returns a context whose classifier calls report the
// provider/model that answered them, and a function that reads it back. The
// reader returns "" when no classifier on the path reported one; the TUI then
// attributes the activity to the agent model, as before.
func TrackServedModel(ctx context.Context) (context.Context, func() string) {
	s := &servedSlot{}
	return context.WithValue(ctx, servedKey{}, s), func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.model
	}
}

// NoteServedModel records, on a context from TrackServedModel, the
// "provider/model" identity that answered a classifier call. A classifier
// implementation calls it once it has a reply; on an untracked context it does
// nothing. The identity is harness-composed configuration, never model output.
func NoteServedModel(ctx context.Context, model string) {
	if s, ok := ctx.Value(servedKey{}).(*servedSlot); ok && model != "" {
		s.mu.Lock()
		s.model = model
		s.mu.Unlock()
	}
}

// classifyServed runs one classifier call and returns its reply with the
// provider/model that answered it ("" when unreported).
func classifyServed(ctx context.Context, c Classifier, p ClassifierPayload) (string, string, error) {
	ctx, served := TrackServedModel(ctx)
	raw, err := c.Classify(ctx, p)
	return raw, served(), err
}
