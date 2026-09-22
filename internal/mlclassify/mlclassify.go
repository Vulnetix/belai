// Package mlclassify holds the in-process security classification stack: small
// BERT sequence classifiers run through cybertron/spaGO as the phase-1 prompt
// saturation gate and the phase-2 jailbreak gate, plus an optional phase-3
// narrowed LLM sentinel for the two extraction categories no purpose-built
// model here can reach.
//
// The stack is wired as a rolemanager.Classifier so the Role Manager pipeline
// stays untouched: it returns a raw sentinel token string, and
// rolemanager.ParseSentinel remains the single parse point.
package mlclassify

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// Phase identifies one local security gate.
type Phase string

const (
	// Phase1 is the prompt-saturation gate. It maps to
	// rolemanager.SentinelPromptInjection when it fires.
	Phase1 Phase = "phase1"
	// Phase2 is the jailbreak gate. It maps to rolemanager.SentinelJailbreak
	// when it fires.
	Phase2 Phase = "phase2"
)

// Sentinel precedence when folding windows and gates into one verdict:
// PROMPT_INJECTION > JAILBREAK > DATA_EXTRACTION > MODEL_EXTRACTION > SAFE.
var sentinelRank = map[rolemanager.Sentinel]int{
	rolemanager.SentinelPromptInjection: 4,
	rolemanager.SentinelJailbreak:       3,
	rolemanager.SentinelDataExtraction:  2,
	rolemanager.SentinelModelExtraction: 1,
	rolemanager.SentinelSafe:            0,
}

// fold keeps the higher-precedence verdict of a and b.
func fold(a, b rolemanager.Sentinel) rolemanager.Sentinel {
	if sentinelRank[b] > sentinelRank[a] {
		return b
	}
	return a
}

// ModelSource names where a phase model's weights come from.
type ModelSource string

const (
	// SourceEmbedded means the weights are go:embed'ed into this binary and
	// extracted to disk once at startup.
	SourceEmbedded ModelSource = "embedded"
	// SourceHuggingFace means the model runs over the HuggingFace inference
	// API using the resolved HuggingFace token.
	SourceHuggingFace ModelSource = "huggingface"
)

// ModelConfig configures one local phase gate.
type ModelConfig struct {
	// ID is the HuggingFace model id, used in cache identity, extraction
	// paths and diagnostics.
	ID string
	// Source selects embedded vs remote weights.
	Source ModelSource
	// Threshold is the attack-probability threshold at or above which the
	// gate fires. Zero or negative means the default (0.5).
	Threshold float64
	// AttackLabel is the classifier label that means "attack" (for example
	// "LABEL_1" on the phase-1 model, "jailbreak" on phase 2). Remote gates
	// need it because they never embed the build-time golden-test mapping.
	AttackLabel string
}

func (m ModelConfig) threshold() float64 {
	if m.Threshold <= 0 {
		return 0.5
	}
	return m.Threshold
}

// WindowConfig bounds token windowing of oversized content. The local models
// hard-error past max_position_embeddings (512) tokens and do not truncate, so
// windowing is the caller's responsibility.
type WindowConfig struct {
	// Tokens is the maximum wordpiece tokens per window, minus [CLS]/[SEP].
	// Zero means 510.
	Tokens int
	// Overlap is the number of tokens adjacent windows share, so an injection
	// straddling a boundary is still seen whole by at least one window. Zero
	// means Tokens/8.
	Overlap int
	// MaxWindows bounds how many windows classify before failing closed.
	// Zero means 64.
	MaxWindows int
}

func (w WindowConfig) tokens() int {
	if w.Tokens <= 0 {
		return 510
	}
	return w.Tokens
}

func (w WindowConfig) overlap() int {
	if w.Overlap <= 0 {
		return w.tokens() / 8
	}
	return w.Overlap
}

func (w WindowConfig) maxWindows() int {
	if w.MaxWindows <= 0 {
		return 64
	}
	return w.MaxWindows
}

// Options configures the ML classifier stack.
type Options struct {
	// Phase1 and Phase2 configure the two local gates. A nil config disables
	// that gate.
	Phase1 *ModelConfig
	Phase2 *ModelConfig
	// Phase3 is the narrowed LLM classifier covering DATA_EXTRACTION and
	// MODEL_EXTRACTION only. nil disables phase 3.
	Phase3 rolemanager.Classifier
	// HFToken resolves the HuggingFace bearer token for remote models.
	HFToken func() (string, error)
	// Window bounds token windowing. Zero fields take defaults.
	Window WindowConfig
}

// Identity returns the classifier-identity string mixed into verdict cache
// keys so verdicts from different classifier stacks never share a bucket.
func (o Options) Identity() string {
	var b strings.Builder
	b.WriteString("models")
	if o.Phase1 != nil {
		fmt.Fprintf(&b, ";phase1=%s@%s:%.3f", o.Phase1.ID, o.Phase1.Source, o.Phase1.threshold())
	}
	if o.Phase2 != nil {
		fmt.Fprintf(&b, ";phase2=%s@%s:%.3f", o.Phase2.ID, o.Phase2.Source, o.Phase2.threshold())
	}
	fmt.Fprintf(&b, ";phase3=%t", o.Phase3 != nil)
	return b.String()
}

// OptionsIdentity returns the verdict-cache identity string for a classifier
// stack described by the two phase configs and phase-3 on/off, without
// building the classifier. It lets the pipeline key the cache consistently
// even when the classifier itself failed to build.
func OptionsIdentity(phase1, phase2 *ModelConfig, phase3On bool) string {
	var phase3 rolemanager.Classifier
	if phase3On {
		phase3 = rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
			return "", nil
		})
	}
	return Options{Phase1: phase1, Phase2: phase2, Phase3: phase3}.Identity()
}

// gate runs one local model over a single already-windowed text.
type gate interface {
	// phase returns the gate's phase.
	phase() Phase
	// fire returns the gate's attack sentinel if the window classifies as an
	// attack at or above threshold, else rolemanager.SentinelSafe. It also
	// returns the attack score for diagnostics.
	fire(ctx context.Context, window string) (rolemanager.Sentinel, float64, error)
}

// span is one token's byte span in the original content.
type span struct{ start, end int }

// tokenizeFunc splits text into wordpiece-token byte spans.
type tokenizeFunc func(text string) []span

// Classifier implements rolemanager.Classifier using the local models, with an
// optional narrowed LLM (phase 3) fallback for extraction verdicts.
type Classifier struct {
	phase1 gate
	phase2 gate
	llm    rolemanager.Classifier
	window WindowConfig
	tok    tokenizeFunc
	ident  string
}

// New builds a Classifier from opts. It resolves each configured gate to its
// concrete implementation: an embedded model extracted from the binary, or a
// remote HuggingFace client. At least one of Phase1/Phase2 must be non-nil.
func New(opts Options) (*Classifier, error) {
	if opts.Phase1 == nil && opts.Phase2 == nil {
		return nil, fmt.Errorf("mlclassify: no phase configured")
	}
	c := &Classifier{
		llm:    opts.Phase3,
		window: opts.Window,
		ident:  opts.Identity(),
	}
	var err error
	if opts.Phase1 != nil {
		c.phase1, err = newGate(Phase1, *opts.Phase1, opts.HFToken)
		if err != nil {
			return nil, fmt.Errorf("mlclassify: phase1: %w", err)
		}
	}
	if opts.Phase2 != nil {
		c.phase2, err = newGate(Phase2, *opts.Phase2, opts.HFToken)
		if err != nil {
			return nil, fmt.Errorf("mlclassify: phase2: %w", err)
		}
	}
	// A single shared tokenizer drives windowing. Prefer a local gate's
	// tokenizer; both phase models use the bert-base-uncased vocabulary, so
	// one tokenizer is sufficient for windowing regardless of which model
	// classifies.
	c.tok = c.windowTokenizer()
	if c.tok == nil {
		return nil, fmt.Errorf("mlclassify: no tokenizer available for windowing")
	}
	return c, nil
}

// windowTokenizer returns a tokenizeFunc for windowing, from the first gate
// that can supply one (local gates embed a tokenizer; remote gates fetch the
// vocabulary).
func (c *Classifier) windowTokenizer() tokenizeFunc {
	if g, ok := c.phase1.(interface{ tokenizer() tokenizeFunc }); ok {
		return g.tokenizer()
	}
	if g, ok := c.phase2.(interface{ tokenizer() tokenizeFunc }); ok {
		return g.tokenizer()
	}
	return nil
}

// Identity returns the classifier-identity string for verdict cache keying.
func (c *Classifier) Identity() string { return c.ident }

// EmbeddedPhase1 reports the embedded phase-1 model id and whether this
// binary variant embeds it.
func EmbeddedPhase1() (string, bool) {
	for _, s := range embeddedSpecs() {
		if s.phase == Phase1 {
			return s.id, true
		}
	}
	return "", false
}

// EmbeddedPhase2 reports the embedded phase-2 model id and whether this
// binary variant embeds it.
func EmbeddedPhase2() (string, bool) {
	if jb, ok := jailbreakEmbeddedSpec(); ok {
		return jb.id, true
	}
	return "", false
}

// Embedded reports whether this binary variant embeds the phase-1 model,
// which makes "models" the natural default classifier kind.
func Embedded() bool {
	_, ok := EmbeddedPhase1()
	return ok
}

// Classify implements rolemanager.Classifier. It windows the content, runs
// phases 1 and 2 concurrently per window, folds fail-closed, and only when
// both clear every window and phase 3 is configured runs the narrowed LLM
// sentinel. It returns a raw sentinel token string; an empty string with a nil
// error signals a malformed phase-3 reply (so rolemanager.ParseSentinel fails
// closed exactly as today).
func (c *Classifier) Classify(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
	windows, err := c.windows(p.User)
	if err != nil {
		return "", err
	}
	var verdict rolemanager.Sentinel = rolemanager.SentinelSafe
	for _, w := range windows {
		s, err := c.classifyWindow(ctx, w)
		if err != nil {
			return "", err
		}
		verdict = fold(verdict, s)
		// PROMPT_INJECTION is the highest rank; no later window can change it.
		if verdict == rolemanager.SentinelPromptInjection {
			return string(verdict), nil
		}
	}
	if verdict != rolemanager.SentinelSafe {
		return string(verdict), nil
	}
	if c.llm == nil {
		return string(rolemanager.SentinelSafe), nil
	}
	return c.phase3(ctx, p.User)
}

// classifyWindow runs phases 1 and 2 concurrently over one window and folds
// the two verdicts. Any gate error is a hard failure.
func (c *Classifier) classifyWindow(ctx context.Context, window string) (rolemanager.Sentinel, error) {
	type result struct {
		s   rolemanager.Sentinel
		err error
	}
	results := make(chan result, 2)
	run := func(g gate) {
		s, _, err := g.fire(ctx, window)
		results <- result{s: s, err: err}
	}
	n := 0
	if c.phase1 != nil {
		n++
		go run(c.phase1)
	}
	if c.phase2 != nil {
		n++
		go run(c.phase2)
	}
	verdict := rolemanager.SentinelSafe
	for i := 0; i < n; i++ {
		r := <-results
		if r.err != nil {
			return "", r.err
		}
		verdict = fold(verdict, r.s)
	}
	return verdict, nil
}

// phase3 runs the narrowed LLM sentinel covering DATA_EXTRACTION and
// MODEL_EXTRACTION only. A malformed reply (including an out-of-scope token
// such as PROMPT_INJECTION) is returned as an empty string so the pipeline's
// ParseSentinel records the malformed event and fails closed.
func (c *Classifier) phase3(ctx context.Context, content string) (string, error) {
	raw, err := c.llm.Classify(ctx, rolemanager.BuildExtractionPayload(content))
	if err != nil {
		return "", err
	}
	s, err := rolemanager.ParseExtractionSentinel(raw)
	if err != nil {
		return "", nil
	}
	return string(s), nil
}

// windows splits content into overlapping token windows no larger than the
// configured token bound, aligned to token boundaries so no window begins or
// ends mid-token. It fails closed when the content would exceed MaxWindows.
func (c *Classifier) windows(content string) ([]string, error) {
	toks := c.tok(content)
	limit := c.window.tokens()
	overlap := c.window.overlap()
	maxWindows := c.window.maxWindows()

	if overlap >= limit {
		overlap = limit / 2
	}

	if len(toks) == 0 {
		return []string{""}, nil
	}
	if len(toks) <= limit {
		return []string{content}, nil
	}

	var windows []string
	for start := 0; start < len(toks); {
		end := start + limit
		if end >= len(toks) {
			windows = append(windows, sliceText(content, toks, start, len(toks)))
			break
		}
		windows = append(windows, sliceText(content, toks, start, end))
		if len(windows) > maxWindows {
			return nil, fmt.Errorf("mlclassify: content exceeds %d windows", maxWindows)
		}
		next := end - overlap
		if next <= start {
			next = start + 1
		}
		start = next
	}
	return windows, nil
}

// sliceText returns the substring of content spanning token [start,end).
func sliceText(content string, toks []span, start, end int) string {
	s := toks[start].start
	e := toks[end-1].end
	if e > len(content) {
		e = len(content)
	}
	return content[s:e]
}
