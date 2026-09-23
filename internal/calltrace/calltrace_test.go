package calltrace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/version"
)

var traceparentRE = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)

func TestApplyNoSession(t *testing.T) {
	h := http.Header{}
	Apply(context.Background(), h)
	if got := h.Get(HeaderClientVersion); got != version.Version {
		t.Fatalf("client version = %q, want %q", got, version.Version)
	}
	if h.Get(HeaderClientBuild) == "" {
		t.Fatal("client build header missing")
	}
	for _, k := range []string{HeaderSessionID, HeaderTool, HeaderToolCallID, HeaderTraceparent} {
		if h.Get(k) != "" {
			t.Fatalf("%s set without a session: %q", k, h.Get(k))
		}
	}
}

func TestApplySessionOnly(t *testing.T) {
	ctx := WithSession(context.Background(), "sess-1")
	h := http.Header{}
	Apply(ctx, h)
	if h.Get(HeaderSessionID) != "sess-1" {
		t.Fatalf("session header = %q", h.Get(HeaderSessionID))
	}
	if h.Get(HeaderTool) != "" || h.Get(HeaderToolCallID) != "" {
		t.Fatal("tool headers set outside a tool call")
	}
	if !traceparentRE.MatchString(h.Get(HeaderTraceparent)) {
		t.Fatalf("traceparent = %q", h.Get(HeaderTraceparent))
	}
}

func TestApplyToolCall(t *testing.T) {
	ctx := WithTool(WithSession(context.Background(), "sess-1"), "WebFetch", "call_9")
	h := http.Header{}
	Apply(ctx, h)
	if h.Get(HeaderTool) != "WebFetch" || h.Get(HeaderToolCallID) != "call_9" {
		t.Fatalf("tool headers = %q / %q", h.Get(HeaderTool), h.Get(HeaderToolCallID))
	}
	if h.Get(HeaderSessionID) != "sess-1" {
		t.Fatal("session lost by WithTool")
	}
}

func TestTraceStableSpanPerCall(t *testing.T) {
	sess := WithSession(context.Background(), "sess-1")
	a, b := http.Header{}, http.Header{}
	Apply(WithTool(sess, "Bash", "1"), a)
	Apply(WithTool(sess, "Bash", "2"), b)
	pa := strings.Split(a.Get(HeaderTraceparent), "-")
	pb := strings.Split(b.Get(HeaderTraceparent), "-")
	if pa[1] != pb[1] {
		t.Fatalf("trace-id differs within a session: %s vs %s", pa[1], pb[1])
	}
	if pa[2] == pb[2] {
		t.Fatal("span-id reused across tool calls")
	}

	// One tool call applied twice keeps its span.
	call := WithTool(sess, "WebSearch", "3")
	c, d := http.Header{}, http.Header{}
	Apply(call, c)
	Apply(call, d)
	if c.Get(HeaderTraceparent) != d.Get(HeaderTraceparent) {
		t.Fatal("span-id changed within one tool call")
	}

	other := http.Header{}
	Apply(WithSession(context.Background(), "sess-2"), other)
	if strings.Split(other.Get(HeaderTraceparent), "-")[1] == pa[1] {
		t.Fatal("different sessions share a trace-id")
	}
}

func TestTokenSanitised(t *testing.T) {
	ctx := WithTool(context.Background(), "Bash", "id\r\nX-Evil: 1\x00é")
	h := http.Header{}
	Apply(ctx, h)
	if got := h.Get(HeaderToolCallID); got != "idX-Evil:1" {
		t.Fatalf("call id = %q", got)
	}
	long := WithTool(context.Background(), "Bash", strings.Repeat("a", 500))
	if got := len(from(long).callID); got != maxToken {
		t.Fatalf("call id length = %d, want %d", got, maxToken)
	}
}

func TestEnv(t *testing.T) {
	base := Env(context.Background())
	if strings.Join(base, " ") != "SIGNET=1 SIGNET_VERSION="+version.Version {
		t.Fatalf("base env = %v", base)
	}
	ctx := WithTool(WithSession(context.Background(), "sess-1"), "Bash", "c1")
	env := map[string]string{}
	for _, kv := range Env(ctx) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env[EnvSessionID] != "sess-1" || env[EnvTool] != "Bash" || env[EnvToolCallID] != "c1" {
		t.Fatalf("env = %v", env)
	}
	if !traceparentRE.MatchString(env[EnvTraceparent]) {
		t.Fatalf("TRACEPARENT = %q", env[EnvTraceparent])
	}
}

func TestEmptySessionNoop(t *testing.T) {
	ctx := context.Background()
	if WithSession(ctx, "") != ctx {
		t.Fatal("empty session should return ctx unchanged")
	}
	if SessionID(ctx) != "" {
		t.Fatal("SessionID on bare ctx")
	}
}

func TestBuildIncludesVariant(t *testing.T) {
	orig := version.Variant
	t.Cleanup(func() { version.Variant = orig })

	version.Variant = ""
	if got, want := Build(), version.Commit+"; "+version.BuildDate; got != want {
		t.Fatalf("Build() = %q, want %q", got, want)
	}
	version.Variant = "bert-guardrails"
	if got := Build(); !strings.HasSuffix(got, "; bert-guardrails") {
		t.Fatalf("Build() = %q, want a variant suffix", got)
	}
}

// TestToolWithoutSession pins the edge where a tool runs with no session: the
// tool headers are stamped, but there is no session id and no traceparent,
// because the trace-id derives from the session.
func TestToolWithoutSession(t *testing.T) {
	h := http.Header{}
	Apply(WithTool(context.Background(), "Glob", ""), h)
	if h.Get(HeaderTool) != "Glob" {
		t.Fatalf("tool = %q", h.Get(HeaderTool))
	}
	if h.Get(HeaderToolCallID) != "" {
		t.Fatal("empty call id must be omitted")
	}
	if h.Get(HeaderSessionID) != "" || h.Get(HeaderTraceparent) != "" {
		t.Fatal("session headers set without a session")
	}
}

// TestProviderSpanPerRequest pins that outside a tool call each Apply mints a
// fresh span in the same trace: two provider requests are two spans.
func TestProviderSpanPerRequest(t *testing.T) {
	ctx := WithSession(context.Background(), "sess-1")
	a, b := http.Header{}, http.Header{}
	Apply(ctx, a)
	Apply(ctx, b)
	pa := strings.Split(a.Get(HeaderTraceparent), "-")
	pb := strings.Split(b.Get(HeaderTraceparent), "-")
	if pa[1] != pb[1] || pa[2] == pb[2] {
		t.Fatalf("want same trace, different span: %v vs %v", pa, pb)
	}
}

// TestTraceIDDerivation pins the documented derivation: the first 16 bytes of
// SHA-256(session id), lowercase hex.
func TestTraceIDDerivation(t *testing.T) {
	sum := sha256.Sum256([]byte("sess-1"))
	h := http.Header{}
	Apply(WithSession(context.Background(), "sess-1"), h)
	if got := strings.Split(h.Get(HeaderTraceparent), "-")[1]; got != hex.EncodeToString(sum[:16]) {
		t.Fatalf("trace-id = %q", got)
	}
}

// TestWithToolKeepsSessionAcrossNesting pins that a nested WithTool replaces
// the tool identity but keeps the session.
func TestWithToolKeepsSessionAcrossNesting(t *testing.T) {
	ctx := WithTool(WithTool(WithSession(context.Background(), "s"), "A", "1"), "B", "2")
	h := http.Header{}
	Apply(ctx, h)
	if h.Get(HeaderSessionID) != "s" || h.Get(HeaderTool) != "B" || h.Get(HeaderToolCallID) != "2" {
		t.Fatalf("headers = %v", h)
	}
	if SessionID(ctx) != "s" {
		t.Fatal("SessionID lost")
	}
}

func TestNilContext(t *testing.T) {
	var nilCtx context.Context
	if SessionID(nilCtx) != "" {
		t.Fatal("nil ctx should carry no session")
	}
}
