package rolemanager

import "context"

// StreamClassifier is a Classifier that can hand over its answer as it is
// written. onText receives each piece of text as it arrives, and the full
// answer is still returned at the end. A caller that shows text while it is
// made uses it; every other caller keeps using Classify.
type StreamClassifier interface {
	Classifier
	ClassifyStream(ctx context.Context, p ClassifierPayload, onText func(delta string)) (string, error)
}

// ClassifyStreaming asks c for its answer, streaming it when c can. A
// classifier that cannot stream answers in one piece: onText is called once
// with the whole answer, so callers need no second code path.
func ClassifyStreaming(ctx context.Context, c Classifier, p ClassifierPayload, onText func(delta string)) (string, error) {
	if s, ok := c.(StreamClassifier); ok {
		return s.ClassifyStream(ctx, p, onText)
	}
	out, err := c.Classify(ctx, p)
	if err == nil && onText != nil && out != "" {
		onText(out)
	}
	return out, err
}
