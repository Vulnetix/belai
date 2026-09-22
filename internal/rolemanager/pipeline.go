package rolemanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/vulnetix/signet/internal/agentpool"
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
	Classify(context.Context, ClassifierPayload) (string, error)
}

// ClassifierFunc adapts a func to Classifier.
type ClassifierFunc func(context.Context, ClassifierPayload) (string, error)

// Classify implements Classifier.
func (f ClassifierFunc) Classify(ctx context.Context, p ClassifierPayload) (string, error) {
	return f(ctx, p)
}

// Pipeline sanitizes and classifies untrusted tool results. The classifier
// turn carries no tools, skills, or agent block, and the pipeline never
// executes tools during classification.
type Pipeline struct {
	Classifier Classifier
	// Chunk bounds the chunked classify-all path for oversized payloads. A
	// zero MaxBytes disables chunking (everything classifies in one call).
	Chunk ChunkConfig
	// Cache, when non-nil, memoises verdicts by the SHA-256 of the sanitized
	// content (session-scoped for SAFE, persisted for non-SAFE).
	Cache *Cache
	// Pool is the shared FIFO fan-out pool the Role Manager owns. Explore
	// subagents and background agents acquire a lease through AcquireAgent so
	// admission is traced with the rolemanager record helper. nil means no
	// shared ceiling.
	Pool *agentpool.Pool
}

// ChunkConfig bounds chunked classification of oversized content. Content over
// MaxBytes is split into overlapping chunks and classified concurrently; any
// non-SAFE (or malformed) chunk fails the whole content closed.
type ChunkConfig struct {
	// MaxBytes is the size over which content is chunked. Zero disables.
	MaxBytes int
	// Concurrency caps how many chunks classify in parallel. Zero means 4.
	Concurrency int
	// Overlap is the byte window each adjacent chunk shares, so an injection
	// straddling a boundary is still seen whole by one chunk. Zero means a
	// default overlap (1/8 of MaxBytes).
	Overlap int
}

// NewPipeline returns a Pipeline using the given classifier.
func NewPipeline(c Classifier) *Pipeline {
	return &Pipeline{Classifier: c}
}

// NewPipelineWithChunk returns a Pipeline using the given classifier and
// chunk configuration.
func NewPipelineWithChunk(c Classifier, chunk ChunkConfig) *Pipeline {
	return &Pipeline{Classifier: c, Chunk: chunk}
}

// run performs sanitize -> classify -> parse and returns the raw result.
// Content over the chunk threshold is classified in overlapping, concurrent
// chunks and folded fail-closed. subject names what was checked (the trace
// Tool field), so a security record can say what it looked at.
func (p *Pipeline) run(ctx context.Context, content, subject string) (clean string, s Sentinel, parsed bool, err error) {
	clean = sanitize.Sanitize(content)
	// Content that is empty (or only whitespace) carries nothing to classify:
	// a shell command that printed nothing cannot hold an injection. Calling
	// the classifier anyway spends a round trip per silent command and sends a
	// user message with no content, which providers reject with a 400.
	if strings.TrimSpace(clean) == "" {
		return clean, SentinelSafe, true, nil
	}
	if p.Cache != nil {
		key := Key(clean)
		if cached, ok := p.Cache.Get(key); ok {
			record(EventVerdictCacheHit, string(cached), subject, "", 0)
			return clean, cached, true, nil
		}
	}

	if p.Chunk.MaxBytes > 0 && len(clean) > p.Chunk.MaxBytes {
		s, parsed, err := p.classifyChunked(ctx, clean)
		if err != nil {
			return clean, "", false, err
		}
		if parsed && p.Cache != nil {
			_ = p.Cache.Put(Key(clean), s)
		}
		return clean, s, parsed, nil
	}

	payload := BuildClassifierPayload(clean)

	raw, err := p.Classifier.Classify(ctx, payload)
	if err != nil {
		return clean, "", false, err
	}

	s, err = ParseSentinel(raw)
	if err != nil {
		record(EventSecuritySentinelMalformed, "", subject, "", 0)
		return clean, "", false, nil
	}
	record(EventSecuritySentinel, string(s), subject, "", 0)
	if p.Cache != nil {
		_ = p.Cache.Put(Key(clean), s)
	}
	return clean, s, true, nil
}

// classifyChunked classifies oversized content in overlapping chunks and folds
// the verdicts fail-closed: any non-SAFE sentinel makes the whole content
// unsafe, and any malformed classifier reply makes the whole result malformed.
func (p *Pipeline) classifyChunked(ctx context.Context, content string) (Sentinel, bool, error) {
	chunks := splitChunks(content, p.Chunk.MaxBytes, p.Chunk.Overlap)
	concurrency := p.Chunk.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}

	type result struct {
		s      Sentinel
		parsed bool
	}
	results := make([]result, len(chunks))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for i, chunk := range chunks {
		wg.Add(1)
		go func(i int, chunk string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			raw, err := p.Classifier.Classify(ctx, BuildClassifierPayload(chunk))
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			s, err := ParseSentinel(raw)
			results[i] = result{s: s, parsed: err == nil}
		}(i, chunk)
	}
	wg.Wait()
	if firstErr != nil {
		return "", false, firstErr
	}

	var nonSafe Sentinel
	allParsed := true
	for _, r := range results {
		if !r.parsed {
			allParsed = false
			continue
		}
		if !r.s.IsSafe() && nonSafe == "" {
			nonSafe = r.s
		}
	}
	if !allParsed {
		return "", false, nil
	}
	if nonSafe != "" {
		return nonSafe, true, nil
	}
	return SentinelSafe, true, nil
}

// splitChunks splits content into overlapping byte chunks no larger than
// maxBytes. Boundaries are aligned to rune starts so no chunk begins or ends
// mid-rune. Adjacent chunks overlap by overlap bytes (defaulting to 1/8 of
// maxBytes when zero) so an injection straddling a boundary is still seen
// whole by at least one chunk.
func splitChunks(content string, maxBytes, overlap int) []string {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	if overlap <= 0 {
		overlap = maxBytes / 8
	}
	if overlap >= maxBytes {
		overlap = maxBytes / 8
	}
	if len(content) <= maxBytes {
		return []string{content}
	}

	var chunks []string
	for start := 0; start < len(content); {
		end := start + maxBytes
		if end >= len(content) {
			chunks = append(chunks, content[start:])
			break
		}
		for end < len(content) && !utf8.RuneStart(content[end]) {
			end++
		}
		chunks = append(chunks, content[start:end])

		next := end - overlap
		if next <= start {
			next = start + 1
		}
		for next > 0 && next < len(content) && !utf8.RuneStart(content[next]) {
			next--
		}
		start = next
	}
	return chunks
}

// Process runs a tool result through sanitize -> classifier -> sentinel.
// SAFE yields ActionProceed; every other sentinel — and any malformed
// classifier output — fails closed to ActionWarn.
func (p *Pipeline) Process(ctx context.Context, r tools.Result) (Decision, error) {
	clean, s, parsed, err := p.run(ctx, r.Content, string(r.Kind))
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

// AcquireAgent admits one fan-out item through the shared FIFO pool and traces
// the admission with the rolemanager record helper. A nil Pool returns a nil
// lease and nil error so callers can run unbounded when no ceiling is set.
func (p *Pipeline) AcquireAgent(ctx context.Context, h agentpool.Handle) (*agentpool.Lease, error) {
	if p.Pool == nil {
		return nil, nil
	}
	lease, err := p.Pool.Acquire(ctx, h)
	if err == nil {
		record(EventAgentPoolAdmit, string(h.State), "", fmt.Sprintf("kind=%s id=%s slot=%d", h.Kind, h.ID, h.Index), 0)
	}
	return lease, err
}

// SnapshotAgents returns the pool's current roster, or nil when no pool is set.
func (p *Pipeline) SnapshotAgents() []agentpool.Handle {
	if p.Pool == nil {
		return nil
	}
	return p.Pool.Snapshot()
}

// CancelAgent cancels one fan-out item by id. A nil Pool reports false.
func (p *Pipeline) CancelAgent(id string) bool {
	if p.Pool == nil {
		return false
	}
	return p.Pool.Cancel(id)
}

// Admit classifies an arbitrary piece of content (e.g. a user prompt) and
// applies the posture policy. Under enforce a non-SAFE sentinel is refused.
// subject names what was checked for the activity feed and the trace file.
func (p *Pipeline) Admit(ctx context.Context, content, subject string, pol posture.Policy) (Decision, error) {
	if pol.Level(posture.PromptUnsafe) == posture.Ignore && pol.Level(posture.PromptMalformed) == posture.Ignore {
		return Decision{Action: ActionProceed, Content: content}, nil
	}
	clean, s, parsed, err := p.run(ctx, content, subject)
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
	return fmt.Sprintf("refusing prompt: %s", e.Sentinel.Label())
}
