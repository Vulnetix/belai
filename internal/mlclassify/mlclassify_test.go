package mlclassify

import (
	"context"
	"strings"
	"testing"

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
	// tokens: 8. limit 3, overlap 1 -> windows of 3 tokens each advancing 2.
	var all []string
	for _, w := range got {
		all = append(all, w)
	}
	if len(all) != 4 {
		t.Fatalf("windows = %q (%d), want 4 windows", all, len(all))
	}
	if all[0] != "a b c" || all[1] != "c d e" || all[2] != "e f g" || all[3] != "g h" {
		t.Fatalf("windows = %q", all)
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
			return "PROMPT_INJECTION", nil // out of scope for phase 3
		}), WindowConfig{Tokens: 3, Overlap: 1, MaxWindows: 10})
	got, err := c.Classify(context.Background(), rolemanager.BuildClassifierPayload("a b c"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(rolemanager.SentinelSafe) {
		t.Fatalf("got %q, want SAFE (inconclusive phase 3 must not block)", got)
	}
}

func TestOptionsIdentityIncludesPhase3State(t *testing.T) {
	p1 := &ModelConfig{ID: "GuardrailsAI/prompt-saturation-attack-detector", Source: SourceEmbedded, Threshold: 0.5}
	on := OptionsIdentity(p1, nil, true)
	off := OptionsIdentity(p1, nil, false)
	if on == off {
		t.Fatalf("phase-3 on/off must produce distinct identities, both %q", on)
	}
	if !strings.Contains(on, "phase3=true") || !strings.Contains(off, "phase3=false") {
		t.Fatalf("identity missing phase3 marker: on=%q off=%q", on, off)
	}
	// Two different phase-1 models must not collide.
	p2 := &ModelConfig{ID: "other/model", Source: SourceEmbedded, Threshold: 0.5}
	if OptionsIdentity(p1, nil, false) == OptionsIdentity(p2, nil, false) {
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
