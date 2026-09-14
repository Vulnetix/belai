package rolemanager

import (
	"fmt"
	"strings"
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

// ParseSentinel maps a raw classifier output to a Sentinel. It accepts only an
// exact sentinel token (surrounding whitespace is trimmed) and rejects any
// other output, including explanations or multiple tokens.
func ParseSentinel(raw string) (Sentinel, error) {
	s := Sentinel(strings.TrimSpace(raw))
	switch s {
	case SentinelSafe, SentinelPromptInjection, SentinelJailbreak, SentinelDataExtraction, SentinelModelExtraction:
		return s, nil
	default:
		return "", fmt.Errorf("malformed classifier output %q: want a single sentinel token", raw)
	}
}

// IsSafe reports whether the sentinel marks content as verified-safe.
func (s Sentinel) IsSafe() bool { return s == SentinelSafe }
