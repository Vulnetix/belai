package tts

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestTTSPageNamesEveryExportedFunction keeps docs/tts.md in step with the
// code: an exported function nobody documented fails here.
func TestTTSPageNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, "docs/tts.md", docparity.ExportedFuncs(t, "."))
}

// TestTTSPageStatesTheLimitsTheCodeEnforces keeps the documented numbers equal
// to the constants.
func TestTTSPageStatesTheLimitsTheCodeEnforces(t *testing.T) {
	doc := docparity.Read(t, "docs/tts.md")
	for _, want := range []string{
		fmt.Sprintf("about %d characters", GroupChars),
		"12,000 characters",
		fmt.Sprintf("up to four requests at once (`Workers`)"),
		fmt.Sprintf("cache_mb` (default %d)", DefaultCacheMB),
		edgeHost,
		DefaultVoice,
		"raw 24 kHz 16-bit mono",
		"SHA-256",
		"mode 0700",
		"1, 2 and 4 seconds",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/tts.md does not state %q", want)
		}
	}
	if Workers != 4 || MaxChars != 12000 {
		t.Fatalf("the docs say 4 workers and 12,000 characters, the code says %d and %d", Workers, MaxChars)
	}
}

// TestTTSPageNamesEveryState keeps the states the card shows complete.
func TestTTSPageNamesEveryState(t *testing.T) {
	doc := docparity.Read(t, "docs/tts.md")
	for s := Idle; s <= Failed; s++ {
		if !strings.Contains(doc, s.String()) {
			t.Errorf("docs/tts.md does not mention the %q state", s)
		}
	}
}

// TestTTSPageListsEveryPlaybackHelper keeps the helper list honest.
func TestTTSPageListsEveryPlaybackHelper(t *testing.T) {
	doc := docparity.Read(t, "docs/tts.md")
	for _, h := range helpers() {
		if !strings.Contains(doc, "`"+h.name+"`") {
			t.Errorf("docs/tts.md does not name the helper %q", h.name)
		}
	}
}

// TestTTSDefaultsMatchTheConfig keeps the two copies of a default in step.
func TestTTSDefaultsMatchTheConfig(t *testing.T) {
	if DefaultCacheMB != 256 {
		t.Fatalf("DefaultCacheMB = %d", DefaultCacheMB)
	}
}

// TestTTSPageStatesThePlayerAndRetryNumbers pins the level bar length, the
// write size and lead of the player and the retry rules.
func TestTTSPageStatesThePlayerAndRetryNumbers(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/tts.md")), " ")
	for _, want := range []string{
		"The loudness of the last 24 readings (about 1.2 seconds)",
		"writes 50 ms of audio at a time",
		"about 150 ms ahead of the clock",
		"retried up to three times, waiting 1, 2 and 4 seconds (a wait never exceeds 8)",
		"A 401, or a 403 with no `Date`, is not retried",
		"`cache_mb` | `256`; from 0 to 4096",
		"`speed` | `1`; from 0.5 to 3",
		"letters, digits and `-`, up to 64",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/tts.md does not say %q", want)
		}
	}
	if Levels != 24 || chunkSamples != SampleRate/20 || lead != 150*time.Millisecond {
		t.Errorf("Levels %d, chunk %d samples, lead %v disagree with the page", Levels, chunkSamples, lead)
	}
	// 24 readings of one 50 ms chunk each is the 1.2 seconds the page states.
	if time.Duration(Levels)*50*time.Millisecond != 1200*time.Millisecond {
		t.Errorf("%d levels of 50 ms is not 1.2 s", Levels)
	}
}
