package rolemanager

import (
	"fmt"

	"github.com/vulnetix/signet/internal/posture"
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

// run performs sanitize -> classify -> parse and returns the raw result.
func (p *Pipeline) run(content string) (clean string, s Sentinel, parsed bool, err error) {
	clean = sanitize.Sanitize(content)
	payload := BuildClassifierPayload(clean)

	raw, err := p.Classifier.Classify(payload)
	if err != nil {
		return clean, "", false, err
	}

	s, err = ParseSentinel(raw)
	if err != nil {
		return clean, "", false, nil
	}
	return clean, s, true, nil
}

// Process runs a tool result through sanitize -> classifier -> sentinel.
// SAFE yields ActionProceed; every other sentinel — and any malformed
// classifier output — fails closed to ActionWarn.
func (p *Pipeline) Process(r tools.Result) (Decision, error) {
	clean, s, parsed, err := p.run(r.Content)
	if err != nil {
		return Decision{}, err
	}
	if !parsed {
		return Decision{Kind: r.Kind, Action: ActionWarn, Content: clean}, nil
	}
	action := ActionWarn
	if s.IsSafe() {
		action = ActionProceed
	}
	return Decision{Kind: r.Kind, Action: action, Sentinel: s, Content: clean}, nil
}

// Admit classifies an arbitrary piece of content (e.g. a user prompt) and
// applies the posture policy. Under enforce a non-SAFE sentinel is refused.
func (p *Pipeline) Admit(content string, pol posture.Policy) (Decision, error) {
	if pol.Level(posture.PromptUnsafe) == posture.Ignore && pol.Level(posture.PromptMalformed) == posture.Ignore {
		return Decision{Action: ActionProceed, Content: content}, nil
	}
	clean, s, parsed, err := p.run(content)
	if err != nil {
		return Decision{}, err
	}
	if !parsed {
		if pol.Level(posture.PromptMalformed) == posture.Ignore {
			return Decision{Action: ActionProceed, Content: clean}, nil
		}
		if pol.Level(posture.PromptMalformed) == posture.Warn {
			return Decision{Action: ActionWarn, Content: clean}, nil
		}
		return Decision{}, &RefusalError{Sentinel: SentinelMalformed}
	}
	if s.IsSafe() {
		return Decision{Action: ActionProceed, Sentinel: s, Content: clean}, nil
	}
	if pol.Level(posture.PromptUnsafe) == posture.Ignore {
		return Decision{Action: ActionProceed, Sentinel: s, Content: clean}, nil
	}
	if pol.Level(posture.PromptUnsafe) == posture.Warn {
		return Decision{Action: ActionWarn, Sentinel: s, Content: clean}, nil
	}
	return Decision{}, &RefusalError{Sentinel: s}
}

// SentinelMalformed is the sentinel used when a refusal error carries no
// parseable sentinel.
const SentinelMalformed Sentinel = "MALFORMED"

// RefusalError is returned by Admit when a prompt is refused.
type RefusalError struct {
	Sentinel Sentinel
}

// Error contains the sentinel token so that callers can assert on it.
func (e *RefusalError) Error() string {
	return fmt.Sprintf("refusing prompt: classified as %s", e.Sentinel)
}
