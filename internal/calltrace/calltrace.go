// Package calltrace carries per-session and per-tool-call trace identity on a
// context and stamps it onto outbound HTTP requests and subprocess
// environments.
//
// Every outbound request already identifies Belai through
// version.UserAgent(). calltrace adds the correlation a server, proxy or child
// process needs to tie a request back to the session and tool call that made
// it, following the conventions other coding agents use (Claude Code's
// X-Claude-Code-Session-Id, Codex's session_id/originator/version) under a
// Belai-namespaced X-Belai-* prefix, plus the vendor-neutral W3C Trace
// Context traceparent header (and the OpenTelemetry TRACEPARENT environment
// variable for child processes).
//
// The values are identity metadata only — never credentials. A context with
// no session still stamps the client version and build, so every call site
// can apply unconditionally.
package calltrace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/vulnetix/belai/internal/version"
)

// Header names stamped onto outbound HTTP requests.
const (
	HeaderSessionID     = "X-Belai-Session-Id"
	HeaderTool          = "X-Belai-Tool"
	HeaderToolCallID    = "X-Belai-Tool-Call-Id"
	HeaderClientVersion = "X-Belai-Client-Version"
	HeaderClientBuild   = "X-Belai-Client-Build"
	HeaderTraceparent   = "traceparent"
)

// Environment variable names exported to tool subprocesses.
const (
	EnvMarker      = "BELAI"
	EnvSessionID   = "BELAI_SESSION_ID"
	EnvTool        = "BELAI_TOOL"
	EnvToolCallID  = "BELAI_TOOL_CALL_ID"
	EnvVersion     = "BELAI_VERSION"
	EnvTraceparent = "TRACEPARENT"
)

// info is the trace identity carried on a context.
type info struct {
	sessionID string
	traceID   string // 32 lowercase hex chars; empty without a session
	tool      string
	callID    string
	spanID    string // 16 lowercase hex chars; empty outside a tool call
}

type ctxKey struct{}

func from(ctx context.Context) info {
	if ctx == nil {
		return info{}
	}
	v, _ := ctx.Value(ctxKey{}).(info)
	return v
}

// WithSession returns ctx carrying sessionID. The W3C trace-id is derived
// from it (the first 16 bytes of its SHA-256), so every request a session
// makes shares one trace. An empty sessionID returns ctx unchanged.
func WithSession(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	sum := sha256.Sum256([]byte(sessionID))
	return context.WithValue(ctx, ctxKey{}, info{
		sessionID: sessionID,
		traceID:   hex.EncodeToString(sum[:16]),
	})
}

// WithTool returns ctx carrying the tool name and the model's tool-call id,
// with a fresh span-id for this call. The session, if any, is kept.
func WithTool(ctx context.Context, tool, callID string) context.Context {
	v := from(ctx)
	v.tool = token(tool)
	v.callID = token(callID)
	v.spanID = newSpanID()
	return context.WithValue(ctx, ctxKey{}, v)
}

// SessionID returns the session id carried on ctx, or "".
func SessionID(ctx context.Context) string { return from(ctx).sessionID }

// TraceID returns the W3C trace-id carried on ctx, or "".
func TraceID(ctx context.Context) string { return from(ctx).traceID }

// SpanID returns the span-id of the tool call carried on ctx, or "".
func SpanID(ctx context.Context) string { return from(ctx).spanID }

// Build returns the client build string: "<commit>; <build date>" plus
// "; <variant>" when the binary has one.
func Build() string {
	b := version.Commit + "; " + version.BuildDate
	if version.Variant != "" {
		b += "; " + version.Variant
	}
	return b
}

// Apply stamps the trace headers for ctx onto h. The client version and build
// are always set; the session, tool and traceparent headers only when ctx
// carries them. Outside a tool call each Apply mints its own span-id, so two
// provider requests in one session are two spans of one trace.
func Apply(ctx context.Context, h http.Header) {
	h.Set(HeaderClientVersion, version.Version)
	h.Set(HeaderClientBuild, Build())
	v := from(ctx)
	if v.sessionID != "" {
		h.Set(HeaderSessionID, v.sessionID)
	}
	if v.tool != "" {
		h.Set(HeaderTool, v.tool)
	}
	if v.callID != "" {
		h.Set(HeaderToolCallID, v.callID)
	}
	if tp := traceparent(v); tp != "" {
		h.Set(HeaderTraceparent, tp)
	}
}

// Env returns the KEY=VALUE pairs to append to a tool subprocess's
// environment. BELAI=1 and BELAI_VERSION are always present.
func Env(ctx context.Context) []string {
	v := from(ctx)
	env := []string{EnvMarker + "=1", EnvVersion + "=" + version.Version}
	if v.sessionID != "" {
		env = append(env, EnvSessionID+"="+v.sessionID)
	}
	if v.tool != "" {
		env = append(env, EnvTool+"="+v.tool)
	}
	if v.callID != "" {
		env = append(env, EnvToolCallID+"="+v.callID)
	}
	if tp := traceparent(v); tp != "" {
		env = append(env, EnvTraceparent+"="+tp)
	}
	return env
}

// traceparent renders the W3C Trace Context header (version 00, sampled), or
// "" without a session.
func traceparent(v info) string {
	if v.traceID == "" {
		return ""
	}
	span := v.spanID
	if span == "" {
		span = newSpanID()
	}
	return "00-" + v.traceID + "-" + span + "-01"
}

// newSpanID returns 8 random bytes as hex. W3C forbids the all-zero id; the
// chance of crypto/rand producing it is negligible but it is rejected anyway.
func newSpanID() string {
	var b [8]byte
	for {
		_, _ = rand.Read(b[:])
		if b != [8]byte{} {
			return hex.EncodeToString(b[:])
		}
	}
}

// maxToken caps a carried tool name or call id.
const maxToken = 128

// token keeps only visible ASCII from a (possibly model-chosen) tool name or
// call id and caps its length, so it is always a valid HTTP header value and a
// single-line environment entry.
func token(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > 0x20 && r < 0x7f {
			b.WriteRune(r)
			if b.Len() >= maxToken {
				break
			}
		}
	}
	return b.String()
}
