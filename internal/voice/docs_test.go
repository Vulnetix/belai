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
