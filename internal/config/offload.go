package config

// OffloadSettings configures tool-result offload (docs/context-offload.md): an
// admitted tool result larger than ThresholdTokens is replaced in the
// conversation by a head-and-tail preview of PreviewTokens plus a reference,
// and the model reads the rest on demand with ReadResult.
//
// Offload changes how much admitted content rides on each request, never what
// is admitted or classified, so any settings layer may set it.
type OffloadSettings struct {
	// Enabled turns offload on. Default on.
	Enabled *bool `json:"enabled,omitempty"`
	// ThresholdTokens is the estimated size above which a result is offloaded.
	ThresholdTokens *int `json:"threshold_tokens,omitempty"`
	// PreviewTokens is the estimated size of the preview that stays inline.
	PreviewTokens *int `json:"preview_tokens,omitempty"`
}

// Offload defaults. The threshold keeps an ordinary build or test run inline
// and offloads the long tail (full logs, large pages, big listings).
const (
	DefaultOffloadThresholdTokens = 4000
	DefaultOffloadPreviewTokens   = 1500
	// minOffloadPreviewTokens keeps a preview useful whatever a layer sets.
	minOffloadPreviewTokens = 200
)

// OffloadEnabled reports whether oversized tool results are offloaded.
// Default on.
func (s Settings) OffloadEnabled() bool {
	return s.Offload == nil || s.Offload.Enabled == nil || *s.Offload.Enabled
}

// OffloadLimits returns the effective threshold and preview sizes in estimated
// tokens. The preview is clamped to at least minOffloadPreviewTokens and the
// threshold to more than the preview, so offloading always shrinks a result.
func (s Settings) OffloadLimits() (threshold, preview int) {
	threshold, preview = DefaultOffloadThresholdTokens, DefaultOffloadPreviewTokens
	if s.Offload != nil {
		if s.Offload.ThresholdTokens != nil && *s.Offload.ThresholdTokens > 0 {
			threshold = *s.Offload.ThresholdTokens
		}
		if s.Offload.PreviewTokens != nil && *s.Offload.PreviewTokens > 0 {
			preview = *s.Offload.PreviewTokens
		}
	}
	preview = max(preview, minOffloadPreviewTokens)
	if threshold <= preview {
		threshold = preview * 2
	}
	return threshold, preview
}

// mergeOffload layers src over dst field by field.
func mergeOffload(dst, src *OffloadSettings) *OffloadSettings {
	if src == nil {
		return dst
	}
	out := OffloadSettings{}
	if dst != nil {
		out = *dst
	}
	if src.Enabled != nil {
		out.Enabled = src.Enabled
	}
	if src.ThresholdTokens != nil {
		out.ThresholdTokens = src.ThresholdTokens
	}
	if src.PreviewTokens != nil {
		out.PreviewTokens = src.PreviewTokens
	}
	return &out
}
