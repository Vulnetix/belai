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
	"time"

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
	// gate fires. Zero or negative means the phase default
	// (DefaultThreshold for phase 1, JailbreakDefaultThreshold for phase 2).
	Threshold float64
	// AttackLabel is the classifier label that means "attack" (for example
	// "LABEL_1" on the phase-1 model, "jailbreak" on phase 2). Remote gates
	// need it because they never embed the build-time golden-test mapping.
	AttackLabel string
}

// DefaultThreshold is the attack-probability threshold phase 1 (the
// prompt-saturation gate) uses when none is configured. It is deliberately
// above the midpoint: the saturation model is effectively binary, so a high
// default favours precision without missing real saturation attacks.
const DefaultThreshold = 0.75

// JailbreakDefaultThreshold is the attack-probability threshold phase 2 (the
// jailbreak gate) uses when none is configured. It is lower than
// DefaultThreshold because the embedded jailbreak model is calibrated to lower
// confidence: benign tool output sits around 0.0–0.12 unsafe while known
// jailbreaks land around 0.6–0.75, so 0.5 separates them with margin. A 0.75
// default would miss the DAN jailbreak outright.
const JailbreakDefaultThreshold = 0.5

// effectiveThreshold returns the attack threshold for a phase, applying the
// phase-specific default when the configured threshold is unset.
func (m ModelConfig) effectiveThreshold(phase Phase) float64 {
	if m.Threshold > 0 {
		return m.Threshold
	}
	if phase == Phase2 {
		return JailbreakDefaultThreshold
	}
	return DefaultThreshold
}

// ThresholdOr returns the effective attack threshold for a phase, defaulting
// when unset. It is the exported form of effectiveThreshold for the TUI, which
// must render the same value the classifier actually enforces.
func (m ModelConfig) ThresholdOr(phase Phase) float64 { return m.effectiveThreshold(phase) }

// maxPositionEmbeddings is the BERT sequence length the embedded models enforce.
// The model counts [CLS]/[SEP], so the content token budget is two less.
const maxPositionEmbeddings = 512

// WindowConfig bounds token windowing of oversized content. The local models
// hard-error past max_position_embeddings and do not truncate, so windowing is
// the caller's responsibility.
type WindowConfig struct {
	// Tokens is the maximum wordpiece tokens per window, minus [CLS]/[SEP] and
	// a small safety margin. The margin covers rare cases where re-tokenizing a
	// substring can produce one or two extra wordpiece tokens because the
	// surrounding context at a window boundary changes how the first/last word
	// is split. Zero means 508.
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
		// Budget: 512 - 2 special tokens - 2 safety margin = 508.
		return maxPositionEmbeddings - 4
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
	// Phase2Deferred reports that no local jailbreak gate runs (this build
	// variant has no embedded phase-2 model and no remote one was configured)
	// and the phase-3 LLM sentinel therefore also covers JAILBREAK. It mirrors
	// run.SecurityClassifierConfig.Phase2Deferred.
	Phase2Deferred bool
	// Phase3Label is the provider/model display identity of the phase-3 LLM
	// classifier (e.g. "openrouter/typesafe/jev-1.13"), recorded to the
	// activity feed so the TUI can show which model ran phase 3.
	Phase3Label string
	// HFToken resolves the HuggingFace bearer token for remote models.
	HFToken func() (string, error)
	// Window bounds token windowing. Zero fields take defaults.
	Window WindowConfig
}

// Identity returns the classifier-identity string mixed into verdict cache
// keys so verdicts from different classifier stacks never share a bucket.
// windowingVersion is mixed into the cache identity. Bump it whenever window
// slicing changes what text a verdict was computed over: version 2 fixed the
// rune/byte offset bug, so every verdict computed on misaligned windows —
// including permanently cached false positives on non-ASCII files — is
// re-evaluated once rather than trusted. Content is still always classified.
const windowingVersion = 2

func (o Options) Identity() string {
	var b strings.Builder
	b.WriteString("models")
	fmt.Fprintf(&b, ";windowing=v%d", windowingVersion)
	if o.Phase1 != nil {
		fmt.Fprintf(&b, ";phase1=%s@%s:%.3f", o.Phase1.ID, o.Phase1.Source, o.Phase1.effectiveThreshold(Phase1))
	}
	if o.Phase2 != nil {
		fmt.Fprintf(&b, ";phase2=%s@%s:%.3f", o.Phase2.ID, o.Phase2.Source, o.Phase2.effectiveThreshold(Phase2))
	}
	fmt.Fprintf(&b, ";phase3=%t", o.Phase3 != nil)
	fmt.Fprintf(&b, ";phase2deferred=%t", o.Phase2Deferred)
	return b.String()
}

// OptionsIdentity returns the verdict-cache identity string for a classifier
// stack described by the two phase configs, phase-3 on/off and the phase-2
// deferred flag, without building the classifier. It lets the pipeline key the
// cache consistently even when the classifier itself failed to build.
func OptionsIdentity(phase1, phase2 *ModelConfig, phase3On, phase2Deferred bool) string {
	var phase3 rolemanager.Classifier
	if phase3On {
		phase3 = rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
			return "", nil
		})
	}
	return Options{Phase1: phase1, Phase2: phase2, Phase3: phase3, Phase2Deferred: phase2Deferred}.Identity()
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
	phase1      gate
	phase2      gate
	llm         rolemanager.Classifier
	window      WindowConfig
	tok         tokenizeFunc
	ident       string
	phase1Label string
	phase2Label string
	phase3Label string
	// phase2Deferred broadens the phase-3 LLM sentinel to cover JAILBREAK.
	phase2Deferred bool
}

// New builds a Classifier from opts. It resolves each configured gate to its
// concrete implementation: an embedded model extracted from the binary, or a
// remote HuggingFace client. At least one of Phase1/Phase2 must be non-nil.
func New(opts Options) (*Classifier, error) {
	if opts.Phase1 == nil && opts.Phase2 == nil {
		return nil, fmt.Errorf("mlclassify: no phase configured")
	}
	c := &Classifier{
		llm:            opts.Phase3,
		window:         opts.Window,
		ident:          opts.Identity(),
		phase3Label:    opts.Phase3Label,
		phase2Deferred: opts.Phase2Deferred,
	}
	var err error
	if opts.Phase1 != nil {
		c.phase1, err = newGate(Phase1, *opts.Phase1, opts.HFToken)
		if err != nil {
			return nil, fmt.Errorf("mlclassify: phase1: %w", err)
		}
		c.phase1Label = string(opts.Phase1.Source) + "/" + opts.Phase1.ID
	}
	if opts.Phase2 != nil {
		c.phase2, err = newGate(Phase2, *opts.Phase2, opts.HFToken)
		if err != nil {
			return nil, fmt.Errorf("mlclassify: phase2: %w", err)
		}
		c.phase2Label = string(opts.Phase2.Source) + "/" + opts.Phase2.ID
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
// sentinel. Each phase's verdict is emitted to the role-manager activity feed
// so the TUI can show phase 1/2/3 when internal work is set to security or
// all. It returns a raw sentinel token string. An inconclusive phase-3 reply
// is returned as SAFE (see phase3), so the primary gates — not the opt-in
// extraction check — decide the verdict.
func (c *Classifier) Classify(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
	windows, err := c.windows(p.User)
	if err != nil {
		return "", err
	}
	var p1, p2 rolemanager.Sentinel = rolemanager.SentinelSafe, rolemanager.SentinelSafe
	// Phases 1 and 2 run concurrently per window, so they share one
	// wall-clock time: the time the gates held the call.
	gatesStart := time.Now()
	for _, w := range windows {
		a, b, err := c.classifyWindow(ctx, w)
		if err != nil {
			return "", err
		}
		p1 = fold(p1, a)
		p2 = fold(p2, b)
		// PROMPT_INJECTION is the highest rank; no later window can change it.
		if p1 == rolemanager.SentinelPromptInjection {
			c.emitPhases(p1, p2, "skipped", time.Since(gatesStart), 0)
			return string(p1), nil
		}
	}
	gates := time.Since(gatesStart)
	verdict := fold(p1, p2)
	if verdict != rolemanager.SentinelSafe {
		c.emitPhases(p1, p2, "skipped", gates, 0)
		return string(verdict), nil
	}
	if c.llm == nil {
		c.emitPhases(p1, p2, "off", gates, 0)
		return string(rolemanager.SentinelSafe), nil
	}
	phase3Start := time.Now()
	s3, status, err := c.phase3(ctx, p.User)
	if err != nil {
		return "", err
	}
	c.emitPhases(p1, p2, status, gates, time.Since(phase3Start))
	return s3, nil
}

// emitPhases records the three phase verdicts. A disabled phase 2 reports
// "off"; a skipped phase 3 means an earlier phase already failed the content.
func (c *Classifier) emitPhases(p1, p2 rolemanager.Sentinel, phase3 string, gates, phase3Took time.Duration) {
	rolemanager.RecordSecurityPhaseTimed("phase 1", string(p1), c.phase1Label, gates)
	if c.phase2 != nil {
		rolemanager.RecordSecurityPhaseTimed("phase 2", string(p2), c.phase2Label, gates)
	} else {
		rolemanager.RecordSecurityPhase("phase 2", "off", c.phase2Label)
	}
	rolemanager.RecordSecurityPhaseTimed("phase 3", phase3, c.phase3Label, phase3Took)
}

// classifyWindow runs phases 1 and 2 concurrently over one window and returns
// each phase's verdict. Any gate error is a hard failure.
func (c *Classifier) classifyWindow(ctx context.Context, window string) (rolemanager.Sentinel, rolemanager.Sentinel, error) {
	type result struct {
		phase Phase
		s     rolemanager.Sentinel
		err   error
	}
	results := make(chan result, 2)
	run := func(g gate) {
		s, _, err := g.fire(ctx, window)
		results <- result{phase: g.phase(), s: s, err: err}
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
	p1, p2 := rolemanager.SentinelSafe, rolemanager.SentinelSafe
	for i := 0; i < n; i++ {
		r := <-results
		if r.err != nil {
			return "", "", r.err
		}
		switch r.phase {
		case Phase1:
			p1 = r.s
		case Phase2:
			p2 = r.s
		}
	}
	return p1, p2, nil
}

// phase3 runs the narrowed LLM sentinel. With a local jailbreak gate it
// covers DATA_EXTRACTION and MODEL_EXTRACTION only; when phase 2 is deferred
// it also covers JAILBREAK. It returns the parsed sentinel and the status word
// the feed shows. A malformed reply (including an out-of-scope token such as
// PROMPT_INJECTION) is inconclusive, not a verdict: phases 1 and 2 already
// ruled on injection and jailbreak, and phase 3 is an opt-in supplement, so an
// inconclusive reply must not block content the primary gates cleared. The
// feed still records the phase as "malformed" so the TUI shows "couldn't tell".
func (c *Classifier) phase3(ctx context.Context, content string) (string, string, error) {
	var raw string
	var err error
	if c.phase2Deferred {
		raw, err = c.llm.Classify(ctx, rolemanager.BuildDeferredExtractionPayload(content))
	} else {
		raw, err = c.llm.Classify(ctx, rolemanager.BuildExtractionPayload(content))
	}
	if err != nil {
		return "", "", err
	}
	var s rolemanager.Sentinel
	if c.phase2Deferred {
		s, err = rolemanager.ParseDeferredExtractionSentinel(raw)
	} else {
		s, err = rolemanager.ParseExtractionSentinel(raw)
	}
	if err != nil {
		return string(rolemanager.SentinelSafe), "malformed", nil
	}
	return string(s), string(s), nil
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

	// The tokenizer reports offsets in runes, not bytes. Slicing the string
	// with them directly misaligned every window on non-ASCII text: windows
	// re-tokenized past 512 tokens (a hard model error that withheld the
	// result) and the file's tail was never classified at all.
	byteAt := runeByteOffsets(content)
	var windows []string
	for start := 0; start < len(toks); {
		end := start + limit
		if end >= len(toks) {
			windows = append(windows, sliceText(content, byteAt, toks, start, len(toks)))
			break
		}
		windows = append(windows, sliceText(content, byteAt, toks, start, end))
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
// Token offsets are rune indices; byteAt maps them to byte offsets.
func sliceText(content string, byteAt []int, toks []span, start, end int) string {
	s := runeToByte(byteAt, toks[start].start)
	e := runeToByte(byteAt, toks[end-1].end)
	if e < s {
		e = s
	}
	return content[s:e]
}

// runeByteOffsets returns, for every rune index i of s, the byte offset where
// that rune starts, plus a final entry len(s) for the end of the string.
func runeByteOffsets(s string) []int {
	out := make([]int, 0, len(s)+1)
	for i := range s {
		out = append(out, i)
	}
	return append(out, len(s))
}

// runeToByte maps a rune index to its byte offset, clamped to the string.
func runeToByte(byteAt []int, r int) int {
	switch {
	case r <= 0:
		return 0
	case r >= len(byteAt):
		return byteAt[len(byteAt)-1]
	default:
		return byteAt[r]
	}
}
