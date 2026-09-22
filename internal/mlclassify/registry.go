package mlclassify

import "fmt"

// newGate resolves one phase config to its concrete gate implementation:
// an embedded model extracted from this binary, or a remote HuggingFace
// inference client.
func newGate(phase Phase, mc ModelConfig, hfToken func() (string, error)) (gate, error) {
	switch mc.Source {
	case SourceEmbedded:
		spec, ok := embeddedSpecFor(mc.ID)
		if !ok {
			return nil, fmt.Errorf("model %q is not embedded in this build variant", mc.ID)
		}
		return newLocalGate(mc, spec)
	case SourceHuggingFace:
		return newRemoteGate(phase, mc, hfToken)
	default:
		return nil, fmt.Errorf("unknown model source %q", mc.Source)
	}
}

// embeddedSpecFor returns the embedded spec for a model id, if this binary
// variant embeds it.
func embeddedSpecFor(id string) (embeddedSpec, bool) {
	for _, s := range embeddedSpecs() {
		if s.id == id {
			return s, true
		}
	}
	return embeddedSpec{}, false
}
