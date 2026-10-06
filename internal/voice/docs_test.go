package voice

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestVoicePageNamesEveryExportedFunction keeps docs/voice.md in step with
// the code: an exported function nobody documented fails here.
func TestVoicePageNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, "docs/voice.md", docparity.ExportedFuncs(t, "."))
	docparity.RequireMentions(t, "docs/voice.md", docparity.ExportedFuncs(t, "asr"))
}

// TestVoicePageStatesThePinnedModel keeps the documented download in step
// with the constants the code enforces.
func TestVoicePageStatesThePinnedModel(t *testing.T) {
	doc := docparity.Read(t, "docs/voice.md")
	for _, want := range []string{ModelRepo, ModelFile, ModelSHA256, "32,166,155"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/voice.md does not state %q", want)
		}
	}
}

// TestVoicePageNamesEveryState keeps the indicator table complete.
func TestVoicePageNamesEveryState(t *testing.T) {
	doc := docparity.Read(t, "docs/voice.md")
	for s := StateOff; s <= StateTranscribing; s++ {
		if !strings.Contains(doc, s.String()) {
			t.Errorf("docs/voice.md does not mention the %q state", s)
		}
	}
}

// TestVoicePageStatesTheTimings pins the durations docs/voice.md gives for the
// recorder, the segmenter and the running guess to the constants, converting
// samples at 16 kHz.
func TestVoicePageStatesTheTimings(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/voice.md")), " ")
	for _, want := range []string{
		"A recording is capped at 28 seconds",
		"the microphone stays open for 300 ms",
		"Every 1.2 seconds of speech the engine recognises everything heard so far again",
		"A guess needs at least 0.6 seconds of speech",
		"opens after 60 ms of speech and closes after one second of quiet",
		"or after ten seconds with no speech at all",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/voice.md does not say %q", want)
		}
	}
	const rate = 16000
	if maxPTT != 28*rate || pttTail != 300*rate/1000 || partialEvery != 12*rate/10 || partialMin != 6*rate/10 {
		t.Errorf("engine timings disagree: maxPTT %d, pttTail %d, partialEvery %d, partialMin %d", maxPTT, pttTail, partialEvery, partialMin)
	}
	if latchQuiet != rate || latchIdle != 10*rate {
		t.Errorf("tap timings disagree: latchQuiet %d, latchIdle %d", latchQuiet, latchIdle)
	}
	// 20 ms frames: three open a segment (60 ms), fifty close it (one second).
	if frameLen*1000/rate != 20 || onsetFrames*20 != 60 || hangoverFrames*20 != 1000 {
		t.Errorf("segmenter timings disagree: %d ms frame, onset %d, hangover %d", frameLen*1000/rate, onsetFrames, hangoverFrames)
	}
}

// deviceCases is the boundary table both internal/voice and internal/config
// must agree on: a device name is 1 to 128 plain characters that do not start
// with - or =.
var deviceCases = map[string]bool{
	strings.Repeat("a", 128): true,
	strings.Repeat("a", 129): false,
	"hw:1,0":                 true,
	"a=b-c":                  true,
	"-f":                     false,
	"=x":                     false,
	"a b":                    false,
	"a;b":                    false,
}

// TestDevicePatternMatchesThePage pins the device rule the page states.
func TestDevicePatternMatchesThePage(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/voice.md")), " ")
	if !strings.Contains(doc, "at most 128, not starting with `-` or `=`") {
		t.Error("docs/voice.md does not state the device rule")
	}
	for d, want := range deviceCases {
		if got := deviceRE.MatchString(d); got != want {
			t.Errorf("deviceRE(%q) = %v, want %v", d, got, want)
		}
	}
}
