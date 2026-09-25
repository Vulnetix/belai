package rolemanager

import (
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/transcript"
)

// Sentinel is the strict single-token output of the classifier model.
type Sentinel string

const (
	SentinelSafe            Sentinel = "SAFE"
	SentinelPromptInjection Sentinel = "PROMPT_INJECTION"
	SentinelJailbreak       Sentinel = "JAILBREAK"
	SentinelDataExtraction  Sentinel = "DATA_EXTRACTION"
	SentinelModelExtraction Sentinel = "MODEL_EXTRACTION"
)

// sentinelBlockTags are the reasoning block wrappers a reasoning model may
// emit around an otherwise-standalone sentinel. An unterminated block
// truncates the rest of the reply, so a sentinel inside unclosed reasoning
// text is never read as a verdict.
var sentinelBlockTags = []string{"think", "thinking", "reasoning"}

// stripReasoningBlocks removes  thinking, <thinking>, and <reasoning> blocks
// wherever they appear. An unterminated block truncates the rest of the
// string.
func stripReasoningBlocks(s string) string {
	for {
		best := -1
		bestTag := ""
		for _, tag := range sentinelBlockTags {
			open := "<" + tag + ">"
			if i := strings.Index(s, open); i >= 0 && (best == -1 || i < best) {
				best = i
				bestTag = tag
			}
		}
		if best == -1 {
			return s
		}
		open := "<" + bestTag + ">"
		closeTag := "</" + bestTag + ">"
		end := strings.Index(s[best+len(open):], closeTag)
		if end == -1 {
			return s[:best]
		}
		s = s[:best] + s[best+len(open)+end+len(closeTag):]
	}
}

// stripFenceLines drops lines that are only a Markdown fence, so a token
// fenced across its own line is still read as standing alone.
func stripFenceLines(s string) string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// normalizeSentinelReply strips  thinking, <thinking>, and <reasoning> blocks
// (an unterminated block truncates the rest), markdown fences, bold markers,
// backticks, and trailing punctuation. It is the shared normalizer behind
// every sentinel parser.
func normalizeSentinelReply(raw string) string {
	s := strings.TrimSpace(raw)
	s = stripReasoningBlocks(s)
	s = stripFenceLines(s)
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t.,;:!?\"'")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// matchSentinel returns the one allowed token the reply states. A token is
// only accepted when it stands alone on a line after normalization; a token
// mentioned inside prose is not a verdict. Zero matches, or two different
// tokens, is an error — ambiguity fails closed.
func matchSentinel(raw string, allowed []string) (string, error) {
	norm := normalizeSentinelReply(raw)
	allow := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allow[a] = true
	}
	found := map[string]bool{}
	for _, line := range strings.Split(norm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if allow[line] {
			found[line] = true
		}
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no sentinel token found in %q", raw)
	}
	if len(found) == 1 {
		for token := range found {
			return token, nil
		}
	}
	return "", fmt.Errorf("ambiguous sentinel reply %q: multiple tokens", raw)
}

// traceSnippet bounds a malformed evaluator reply to a short sanitized excerpt
// for /trace. Sanitizing first strips delimiter markup, so the trace detail
// stays bounded metadata rather than untrusted content.
func traceSnippet(raw string) string {
	return transcript.TruncateRunes(sanitize.Sanitize(raw), 120)
}

// ParseSentinel maps a raw classifier output to a Sentinel. It accepts a
// sentinel token that stands alone after normalizing away reasoning blocks and
// markdown wrappers, and rejects any other output, including explanations or
// multiple tokens.
func ParseSentinel(raw string) (Sentinel, error) {
	s, err := matchSentinel(raw, []string{
		string(SentinelSafe),
		string(SentinelPromptInjection),
		string(SentinelJailbreak),
		string(SentinelDataExtraction),
		string(SentinelModelExtraction),
	})
	if err != nil {
		return "", fmt.Errorf("malformed classifier output %q: want a single sentinel token", raw)
	}
	return Sentinel(s), nil
}

// ParseExtractionSentinel parses a phase-3 classifier reply. It accepts only
// the four tokens the narrowed extraction prompt names — SAFE,
// PROMPT_INJECTION, DATA_EXTRACTION, MODEL_EXTRACTION — and rejects
// everything else, including JAILBREAK, which the local phase-2 gate owns. A
// jailbreak reply from phase 3 is malformed, not a verdict.
func ParseExtractionSentinel(raw string) (Sentinel, error) {
	s, err := matchSentinel(raw, []string{
		string(SentinelSafe),
		string(SentinelPromptInjection),
		string(SentinelDataExtraction),
		string(SentinelModelExtraction),
	})
	if err != nil {
		return "", fmt.Errorf("malformed classifier output %q: want a single sentinel token", raw)
	}
	return Sentinel(s), nil
}

// ParseDeferredExtractionSentinel parses a phase-3 classifier reply when the
// jailbreak gate is deferred to phase 3. No local gate ruled on jailbreak or
// instruction injection, so it accepts all five tokens the deferred prompt
// names and rejects everything else.
func ParseDeferredExtractionSentinel(raw string) (Sentinel, error) {
	s, err := matchSentinel(raw, []string{
		string(SentinelSafe),
		string(SentinelPromptInjection),
		string(SentinelJailbreak),
		string(SentinelDataExtraction),
		string(SentinelModelExtraction),
	})
	if err != nil {
		return "", fmt.Errorf("malformed classifier output %q: want a single sentinel token", raw)
	}
	return Sentinel(s), nil
}

// IsSafe reports whether the sentinel marks content as verified-safe.
func (s Sentinel) IsSafe() bool { return s == SentinelSafe }
