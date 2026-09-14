package rolemanager

import (
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tools"
)

// Action is the outcome of a pipeline run.
type Action string

const (
	// ActionProceed means the content is verified-safe and may be promoted.
	ActionProceed Action = "proceed"
	// ActionWarn means the content failed classification (or could not be
	// verified) and must not be promoted without informing the user.
	ActionWarn Action = "warn"
)

// Decision is the result of running a tool result through the pipeline.
type Decision struct {
	Kind     tools.Kind
	Action   Action
	Sentinel Sentinel
	// Content is the sanitized content (delimiter markup removed).
	Content string
}

// Classifier sends a ClassifierPayload to a model and returns the raw reply.
type Classifier interface {
	Classify(ClassifierPayload) (string, error)
}

// ClassifierFunc adapts a func to Classifier.
type ClassifierFunc func(ClassifierPayload) (string, error)

// Classify implements Classifier.
func (f ClassifierFunc) Classify(p ClassifierPayload) (string, error) { return f(p) }

// Pipeline sanitizes and classifies untrusted tool results. The classifier
// turn carries no tools, skills, or agent block, and the pipeline never
// executes tools during classification.
type Pipeline struct {
	Classifier Classifier
}

// NewPipeline returns a Pipeline using the given classifier.
func NewPipeline(c Classifier) *Pipeline {
	return &Pipeline{Classifier: c}
}

// Process runs a tool result through sanitize -> classifier -> sentinel.
// SAFE yields ActionProceed; every other sentinel — and any malformed
// classifier output — fails closed to ActionWarn.
func (p *Pipeline) Process(r tools.Result) (Decision, error) {
	clean := sanitize.Sanitize(r.Content)
	payload := BuildClassifierPayload(clean)

	raw, err := p.Classifier.Classify(payload)
	if err != nil {
		return Decision{}, err
	}

	s, err := ParseSentinel(raw)
	if err != nil {
		// Fail closed: unparseable output is never treated as safe.
		return Decision{Kind: r.Kind, Action: ActionWarn, Content: clean}, nil
	}

	action := ActionWarn
	if s.IsSafe() {
		action = ActionProceed
	}
	return Decision{Kind: r.Kind, Action: action, Sentinel: s, Content: clean}, nil
}
