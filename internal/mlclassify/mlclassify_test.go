package mlclassify

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vulnetix/signet/internal/rolemanager"
)

type fakeGate struct {
	ph       Phase
	sentinel rolemanager.Sentinel
	err      error
	calls    int
}

func (f *fakeGate) phase() Phase { return f.ph }

func (f *fakeGate) fire(_ context.Context, _ string) (rolemanager.Sentinel, float64, error) {
	f.calls++
	if f.err != nil {
		return "", 0, f.err
	}
	return f.sentinel, 1.0, nil
}

// wordTokenizer splits on spaces so windowing tests can reason about tokens
// without loading a BERT vocabulary.
func wordTokenizer(text string) []span {
	var spans []span
	start := -1
	for i := 0; i <= len(text); i++ {
		if i == len(text) || text[i] == ' ' {
			if start >= 0 {
				spans = append(spans, span{start: start, end: i})
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	return spans
}

func newTestClassifier(t *testing.T, phase1, phase2 *fakeGate, llm rolemanager.Classifier, window WindowConfig) *Classifier {
	t.Helper()
	c := &Classifier{
		llm:    llm,
		window: window,
		tok:    wordTokenizer,
	}
	var g1, g2 gate
	if phase1 != nil {
		g1 = phase1
	}
	if phase2 != nil {
		g2 = phase2
	}
	c.phase1 = g1
	c.phase2 = g2
	return c
}

func TestModelConfigThresholdDefaults(t *testing.T) {
	if got := (ModelConfig{}).ThresholdOr(Phase1); got != DefaultThreshold {
		t.Fatalf("phase-1 default threshold = %.2f, want %.2f", got, DefaultThreshold)
	}
	if got := (ModelConfig{}).ThresholdOr(Phase2); got != JailbreakDefaultThreshold {
		t.Fatalf("phase-2 default threshold = %.2f, want %.2f", got, JailbreakDefaultThreshold)
	}
	if got := (ModelConfig{Threshold: 0.9}).ThresholdOr(Phase1); got != 0.9 {
		t.Fatalf("explicit threshold = %.2f, want 0.9", got)
	}
	if got := (ModelConfig{Threshold: 0.9}).ThresholdOr(Phase2); got != 0.9 {
		t.Fatalf("explicit phase-2 threshold = %.2f, want 0.9", got)
	}
	if got := (ModelConfig{Threshold: -1}).ThresholdOr(Phase1); got != DefaultThreshold {
		t.Fatalf("negative threshold = %.2f, want default %.2f", got, DefaultThreshold)
	}
}

func TestFoldPrecedence(t *testing.T) {
	cases := []struct {
		a, b, want rolemanager.Sentinel
	}{
		{rolemanager.SentinelSafe, rolemanager.SentinelPromptInjection, rolemanager.SentinelPromptInjection},
		{rolemanager.SentinelJailbreak, rolemanager.SentinelPromptInjection, rolemanager.SentinelPromptInjection},
		{rolemanager.SentinelDataExtraction, rolemanager.SentinelJailbreak, rolemanager.SentinelJailbreak},
		{rolemanager.SentinelModelExtraction, rolemanager.SentinelDataExtraction, rolemanager.SentinelDataExtraction},
		{rolemanager.SentinelSafe, rolemanager.SentinelModelExtraction, rolemanager.SentinelModelExtraction},
		{rolemanager.SentinelSafe, rolemanager.SentinelSafe, rolemanager.SentinelSafe},
	}
	for _, c := range cases {
		if got := fold(c.a, c.b); got != c.want {
			t.Errorf("fold(%s,%s) = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestWindowsTokenBoundAndOverlap(t *testing.T) {
	c := &Classifier{window: WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10}, tok: wordTokenizer}
	got := c.mustWindows(t, "a b c d e f g h")
	// tokens: 8. limit 3, overlap 1 -> four levelled windows of 3 tokens,
	// evenly spaced so the last ends on the final token with no short tail.
	var all []string
	for _, w := range got {
		all = append(all, w)
	}
	if len(all) != 4 {
		t.Fatalf("windows = %q (%d), want 4 windows", all, len(all))
	}
	if all[0] != "a b c" || all[1] != "b c d" || all[2] != "d e f" || all[3] != "f g h" {
		t.Fatalf("windows = %q", all)
	}
}

// Windows are levelled: the fewest windows that fit, all the same size, each
// sharing at least the overlap with its neighbour, and together covering every
// token. A greedy split of 100 tokens at 95 left a 5-token tail.
func TestWindowsAreLevelled(t *testing.T) {
	for _, tc := range []struct{ n, limit, overlap, wantK, wantSize int }{
		{100, 95, 11, 2, 56},
		{96, 95, 11, 2, 54},
		{500, 95, 11, 6, 93},
		{1000, 508, 63, 3, 376},
	} {
		c := &Classifier{window: WindowConfig{MaxWindows: 1000}, tok: wordTokenizer}
		toks := make([]string, tc.n)
		for i := range toks {
			toks[i] = "t" + strings.Repeat("x", i%3)
		}
		got, err := c.levelledWindows(strings.Join(toks, " "), tc.limit, tc.overlap, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.wantK {
			t.Fatalf("n=%d limit=%d: %d windows, want %d", tc.n, tc.limit, len(got), tc.wantK)
		}
		covered := 0
		prevEnd := 0
		for i, w := range got {
			size := len(wordTokenizer(w))
			if size != tc.wantSize || size > tc.limit {
				t.Fatalf("n=%d limit=%d: window %d has %d tokens, want %d", tc.n, tc.limit, i, size, tc.wantSize)
			}
			start := i * (tc.n - size) / (len(got) - 1)
			if i > 0 && prevEnd-start < tc.overlap {
				t.Fatalf("n=%d: windows %d/%d overlap %d, want >= %d", tc.n, i-1, i, prevEnd-start, tc.overlap)
			}
			prevEnd = start + size
			covered = prevEnd
		}
		if covered != tc.n {
			t.Fatalf("n=%d: windows end at token %d, want %d", tc.n, covered, tc.n)
		}
	}
}

// Phase 1 classifies ~95-token windows by default (the saturation model scores
// length past ~100 tokens), phase 2 keeps model-length windows, and the phase-1
// window cap scales so both phases fail closed at the same content size.
func TestPhase1WindowDefaults(t *testing.T) {
	w := WindowConfig{}
	if w.phase1Tokens() != 95 || w.phase1Overlap() != 11 {
		t.Fatalf("phase1 window = %d/%d, want 95/11", w.phase1Tokens(), w.phase1Overlap())
	}
	if w.tokens() != 508 {
		t.Fatalf("general window = %d, want 508", w.tokens())
	}
	// 64 windows advancing 445 tokens ~= 28480 tokens; phase 1 advances 84.
	if got := w.phase1MaxWindows(); got != 340 {
		t.Fatalf("phase1MaxWindows = %d, want 340", got)
	}
	// A general window smaller than 95 is used unchanged by phase 1.
	small := WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10}
	if small.phase1Tokens() != 3 || small.phase1Overlap() != 1 || small.phase1MaxWindows() != 10 {
		t.Fatalf("small phase1 window = %d/%d/%d, want 3/1/10", small.phase1Tokens(), small.phase1Overlap(), small.phase1MaxWindows())
	}
}

// windowGate records the size of every window it is asked to classify.
type windowGate struct {
	ph    Phase
	sizes []int
}

func (g *windowGate) phase() Phase { return g.ph }

func (g *windowGate) fire(_ context.Context, w string) (rolemanager.Sentinel, float64, error) {
	g.sizes = append(g.sizes, len(wordTokenizer(w)))
	return rolemanager.SentinelSafe, 0, nil
}

func TestPhasesClassifyTheirOwnWindows(t *testing.T) {
	g1, g2 := &windowGate{ph: Phase1}, &windowGate{ph: Phase2}
	c := &Classifier{phase1: g1, phase2: g2, tok: wordTokenizer}
	if _, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload(strings.Repeat("t ", 600))); err != nil {
		t.Fatal(err)
	}
	for _, s := range g1.sizes {
		if s > 95 {
			t.Fatalf("phase 1 saw a %d-token window, want <= 95 (sizes %v)", s, g1.sizes)
		}
	}
	if len(g2.sizes) != 2 || g2.sizes[0] > 508 {
		t.Fatalf("phase 2 window sizes = %v, want two levelled windows <= 508", g2.sizes)
	}
	if len(g1.sizes) != 8 {
		t.Fatalf("phase 1 windows = %d, want 8 for 600 tokens", len(g1.sizes))
	}
}

func (c *Classifier) mustWindows(t *testing.T, content string) []string {
	t.Helper()
	w, err := c.windows(content)
	if err != nil {
		t.Fatalf("windows: %v", err)
	}
	return w
}

func TestWindowsMaxWindowsFailsClosed(t *testing.T) {
	c := &Classifier{window: WindowConfig{Tokens: 2, Overlap: 1, MaxWindows: 3}, tok: wordTokenizer}
	if _, err := c.windows("a b c d e f g h i j"); err == nil {
		t.Fatal("expected max-windows failure")
	}
}

func TestWindowsShortContentSingleWindow(t *testing.T) {
	c := &Classifier{window: WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10}, tok: wordTokenizer}
	got := c.mustWindows(t, "a b c")
	if len(got) != 1 || got[0] != "a b c" {
		t.Fatalf("windows = %q, want single window", got)
	}
}

// TestWindowsDefaultBudgetLeavesHeadroom guards the default window token limit.
// The embedded BERT models enforce max_position_embeddings=512 and add
// [CLS]/[SEP]; the default therefore cannot be 510 because re-tokenizing a
// substring at a wordpiece boundary can occasionally produce one or two extra
// content tokens. That caused "input sequence too long: 513 > 512" errors on
// long Read tool results. The safety margin keeps every window under the model
// limit.
func TestWindowsDefaultBudgetLeavesHeadroom(t *testing.T) {
	c := &Classifier{window: WindowConfig{}, tok: wordTokenizer}
	if got := c.window.tokens(); got != maxPositionEmbeddings-4 {
		t.Fatalf("default token budget = %d, want %d", got, maxPositionEmbeddings-4)
	}

	// 508 tokens should stay a single window, but 509 must be split because
	// any re-tokenization headroom must not push it over 512 total.
	got := c.mustWindows(t, strings.Repeat("t ", 508))
	if len(got) != 1 {
		t.Fatalf("508-token content should fit one window, got %d", len(got))
	}
	got = c.mustWindows(t, strings.Repeat("t ", 509))
	if len(got) < 2 {
		t.Fatalf("509-token content must be split, got %d windows", len(got))
	}
}

func TestClassifyPhase1FiresSkipsPhase2And3(t *testing.T) {
	p1 := &fakeGate{ph: Phase1, sentinel: rolemanager.SentinelPromptInjection}
	p2 := &fakeGate{ph: Phase2, sentinel: rolemanager.SentinelSafe}
	called := false
	llm := rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		called = true
		return "", nil
	})
	c := newTestClassifier(t, p1, p2, llm, WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelPromptInjection) {
		t.Fatalf("got %q, want PROMPT_INJECTION", got)
	}
	if called {
		t.Fatal("phase 3 must be skipped when phase 1 fires")
	}
}

func TestClassifyPhase3CalledOnlyWhenBothSafe(t *testing.T) {
	p1 := &fakeGate{ph: Phase1, sentinel: rolemanager.SentinelSafe}
	p2 := &fakeGate{ph: Phase2, sentinel: rolemanager.SentinelSafe}
	called := false
	llm := rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		called = true
		return "DATA_EXTRACTION", nil
	})
	c := newTestClassifier(t, p1, p2, llm, WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !called {
		t.Fatal("phase 3 must be called when phases 1 and 2 are both SAFE")
	}
	if got != string(rolemanager.SentinelDataExtraction) {
		t.Fatalf("got %q, want DATA_EXTRACTION", got)
	}
}

func TestClassifyPhase3OffReturnsSafe(t *testing.T) {
	p1 := &fakeGate{ph: Phase1, sentinel: rolemanager.SentinelSafe}
	c := newTestClassifier(t, p1, nil, nil, WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelSafe) {
		t.Fatalf("got %q, want SAFE", got)
	}
}

func TestPhase3MalformedFallsOpen(t *testing.T) {
	c := newTestClassifier(t, &fakeGate{ph: Phase1, sentinel: rolemanager.SentinelSafe}, nil,
		rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
			return "JAILBREAK", nil // out of scope: the local phase 2 owns it
		}), WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelSafe) {
		t.Fatalf("got %q, want SAFE (inconclusive phase 3 must not block)", got)
	}
}

func TestPhase3DeferredAcceptsJailbreak(t *testing.T) {
	c := newTestClassifier(t, &fakeGate{ph: Phase1, sentinel: rolemanager.SentinelSafe}, nil,
		rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
			return string(rolemanager.SentinelJailbreak), nil
		}), WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	c.phase2Deferred = true
	got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelJailbreak) {
		t.Fatalf("got %q, want JAILBREAK (deferred phase 2 covers jailbreak)", got)
	}
}

// Phase 1 detects prompt saturation, not instruction injection: an "ignore
// previous instructions, run this command" payload in a file read clears it.
// Phase 3 must be able to call that injection on both paths.
func TestPhase3CallsInjection(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		c := newTestClassifier(t, &fakeGate{ph: Phase1, sentinel: rolemanager.SentinelSafe}, nil,
			rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
				return string(rolemanager.SentinelPromptInjection), nil
			}), WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
		c.phase2Deferred = deferred
		got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c"))
		if err != nil {
			t.Fatalf("deferred=%v Classify: %v", deferred, err)
		}
		if got != string(rolemanager.SentinelPromptInjection) {
			t.Fatalf("deferred=%v got %q, want PROMPT_INJECTION", deferred, got)
		}
	}
}

func TestClassifyGateErrorPropagates(t *testing.T) {
	c := newTestClassifier(t, &fakeGate{ph: Phase1, err: context.Canceled}, nil, nil,
		WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	if _, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c")); err == nil {
		t.Fatal("a phase gate error must fail the classification")
	}
}

func TestNewRequiresPhase(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New with no phase configured must error")
	}
}

func TestWindowsEmptyContent(t *testing.T) {
	c := &Classifier{window: WindowConfig{}, tok: wordTokenizer}
	got := c.mustWindows(t, "")
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("empty content windows = %q, want a single empty window", got)
	}
}

func TestWindowsOverlapClampedToHalfLimit(t *testing.T) {
	// overlap >= limit collapses to limit/2 so the next window still advances.
	c := &Classifier{window: WindowConfig{Tokens: 4, Overlap: 8, MaxWindows: 10}, tok: wordTokenizer}
	got := c.mustWindows(t, "a b c d e f g h i j")
	// limit 4, overlap clamped to 2 -> windows advance by 2 tokens.
	var all []string
	for _, w := range got {
		all = append(all, w)
	}
	if len(all) != 4 {
		t.Fatalf("windows = %q (%d), want 4", all, len(all))
	}
	if all[0] != "a b c d" || all[1] != "c d e f" {
		t.Fatalf("windows = %q, overlap not clamped to limit/2", all)
	}
}

func TestOptionsIdentityIncludesPhase3State(t *testing.T) {
	p1 := &ModelConfig{ID: "GuardrailsAI/prompt-saturation-attack-detector", Source: SourceEmbedded, Threshold: 0.5}
	on := OptionsIdentity(p1, nil, true, false)
	off := OptionsIdentity(p1, nil, false, false)
	if on == off {
		t.Fatalf("phase-3 on/off must produce distinct identities, both %q", on)
	}
	if !strings.Contains(on, "phase3=true") || !strings.Contains(off, "phase3=false") {
		t.Fatalf("identity missing phase3 marker: on=%q off=%q", on, off)
	}
	// A deferred jailbreak gate changes the phase-3 prompt, so the identity
	// must change too.
	deferred := OptionsIdentity(p1, nil, true, true)
	if deferred == on {
		t.Fatalf("phase-2 deferred/not-deferred must produce distinct identities, both %q", deferred)
	}
	// Two different phase-1 models must not collide.
	p2 := &ModelConfig{ID: "other/model", Source: SourceEmbedded, Threshold: 0.5}
	if OptionsIdentity(p1, nil, false, false) == OptionsIdentity(p2, nil, false, false) {
		t.Fatal("different phase-1 models must not share an identity")
	}
}

func phaseEvents(acts []rolemanager.Activity) []string {
	var out []string
	for _, a := range acts {
		if a.Event == rolemanager.EventSecurityPhase {
			out = append(out, a.Subject+"="+a.Verdict)
		}
	}
	return out
}

func TestClassifyEmitsPhaseEventsOffWhenIdle(t *testing.T) {
	var got []rolemanager.Activity
	cancel := rolemanager.SetObserver(func(a rolemanager.Activity) { got = append(got, a) })
	defer cancel()

	c := newTestClassifier(t, &fakeGate{ph: Phase1, sentinel: rolemanager.SentinelSafe}, nil, nil,
		WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	if _, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c")); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	want := []string{"phase 1=SAFE", "phase 2=off", "phase 3=off"}
	if got := phaseEvents(got); !slicesEqual(got, want) {
		t.Fatalf("phase events = %v, want %v", got, want)
	}
}

func TestClassifyEmitsPhase3SkippedWhenPhase1Fires(t *testing.T) {
	var got []rolemanager.Activity
	cancel := rolemanager.SetObserver(func(a rolemanager.Activity) { got = append(got, a) })
	defer cancel()

	c := newTestClassifier(t,
		&fakeGate{ph: Phase1, sentinel: rolemanager.SentinelPromptInjection},
		&fakeGate{ph: Phase2, sentinel: rolemanager.SentinelSafe},
		rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
			return "DATA_EXTRACTION", nil // must never be called
		}),
		WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	if got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c")); err != nil || got != string(rolemanager.SentinelPromptInjection) {
		t.Fatalf("Classify = %q, %v", got, err)
	}

	want := []string{"phase 1=PROMPT_INJECTION", "phase 2=SAFE", "phase 3=skipped"}
	if got := phaseEvents(got); !slicesEqual(got, want) {
		t.Fatalf("phase events = %v, want %v", got, want)
	}
}

func TestClassifyEmitsPhase3MalformedStatus(t *testing.T) {
	var got []rolemanager.Activity
	cancel := rolemanager.SetObserver(func(a rolemanager.Activity) { got = append(got, a) })
	defer cancel()

	c := newTestClassifier(t,
		&fakeGate{ph: Phase1, sentinel: rolemanager.SentinelSafe},
		nil,
		rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
			return "not a sentinel", nil // malformed phase-3 reply
		}),
		WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	if got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c")); err != nil || got != string(rolemanager.SentinelSafe) {
		t.Fatalf("Classify = %q, %v; want SAFE when phase 3 is inconclusive", got, err)
	}

	want := []string{"phase 1=SAFE", "phase 2=off", "phase 3=malformed"}
	if got := phaseEvents(got); !slicesEqual(got, want) {
		t.Fatalf("phase events = %v, want %v", got, want)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runeWordTokenizer is wordTokenizer with rune offsets, which is what the
// cybertron wordpiece tokenizer reports.
func runeWordTokenizer(text string) []span {
	var spans []span
	start := -1
	runes := []rune(text)
	for i := 0; i <= len(runes); i++ {
		if i == len(runes) || runes[i] == ' ' {
			if start >= 0 {
				spans = append(spans, span{start: start, end: i})
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	return spans
}

// TestWindowsNonASCIIUseRuneOffsets is the "513 > 512" regression: rune
// offsets used as byte offsets shifted every window on non-ASCII text and
// left the tail unclassified. Each window must be exactly its tokens, and the
// last window must reach the final token.
func TestWindowsNonASCIIUseRuneOffsets(t *testing.T) {
	c := &Classifier{window: WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10}, tok: runeWordTokenizer}
	got := c.mustWindows(t, "α — β → γ 🙂 δ 中文 end")
	want := []string{"α — β", "β → γ", "γ 🙂 δ", "δ 中文 end"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("windows = %q, want %q", got, want)
	}
	for _, w := range got {
		if !utf8.ValidString(w) {
			t.Fatalf("window %q is not valid UTF-8", w)
		}
	}
}
