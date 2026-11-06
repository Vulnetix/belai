package config

// SideSettings governs /btw, a side question answered off the main history
// (docs/side-question.md). A nil field means the built-in default.
type SideSettings struct {
	// Enabled switches /btw off. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// HistoryTurns is how many of the latest user and assistant messages the
	// side question may read besides the harness summary. Default 6, clamped
	// to 0 through 20.
	HistoryTurns *int `json:"history_turns,omitempty"`
	// MaxReplyBytes caps the answer that is shown. Default 4096, clamped to
	// 512 through 8192.
	MaxReplyBytes *int `json:"max_reply_bytes,omitempty"`
}

// Side question bounds.
const (
	DefaultSideHistoryTurns  = 6
	MaxSideHistoryTurns      = 20
	DefaultSideMaxReplyBytes = 4096
	MinSideMaxReplyBytes     = 512
	MaxSideMaxReplyBytes     = 8192
)

// SideEnabled reports whether /btw is available. Default on.
func (s Settings) SideEnabled() bool {
	return s.Side == nil || s.Side.Enabled == nil || *s.Side.Enabled
}

// SideHistoryTurns is how many recent conversation messages a side question
// may read: the set value clamped to its bounds, otherwise the default.
func (s Settings) SideHistoryTurns() int {
	if s.Side == nil || s.Side.HistoryTurns == nil {
		return DefaultSideHistoryTurns
	}
	return clampSideTurns(*s.Side.HistoryTurns)
}

// SideMaxReplyBytes is the size cap of a shown answer: the set value clamped
// to its bounds, otherwise the default.
func (s Settings) SideMaxReplyBytes() int {
	if s.Side == nil || s.Side.MaxReplyBytes == nil {
		return DefaultSideMaxReplyBytes
	}
	return clampSideReply(*s.Side.MaxReplyBytes)
}

func clampSideTurns(n int) int {
	return min(max(n, 0), MaxSideHistoryTurns)
}

func clampSideReply(n int) int {
	return min(max(n, MinSideMaxReplyBytes), MaxSideMaxReplyBytes)
}

// mergeSide folds from over base. A user layer sets any key. The project
// layer, which a cloned repository controls, may turn /btw off and lower the
// two limits, and never enable it or raise a limit.
func mergeSide(base, from *SideSettings, project bool) *SideSettings {
	if from == nil {
		return base
	}
	out := &SideSettings{}
	if base != nil {
		*out = *base
	}
	if from.Enabled != nil && (!project || !*from.Enabled) {
		v := *from.Enabled
		out.Enabled = &v
	}
	cur := Settings{Side: out}
	if from.HistoryTurns != nil {
		v := clampSideTurns(*from.HistoryTurns)
		if !project || v < cur.SideHistoryTurns() {
			out.HistoryTurns = &v
		}
	}
	if from.MaxReplyBytes != nil {
		v := clampSideReply(*from.MaxReplyBytes)
		if !project || v < cur.SideMaxReplyBytes() {
			out.MaxReplyBytes = &v
		}
	}
	return out
}

// sideIgnored names the project keys mergeSide would not apply on top of
// user, for the note Resolve prints.
func sideIgnored(user, proj *SideSettings) []string {
	if proj == nil {
		return nil
	}
	var out []string
	cur := Settings{Side: user}
	if proj.Enabled != nil && *proj.Enabled && !cur.SideEnabled() {
		out = append(out, "side.enabled")
	}
	if proj.HistoryTurns != nil && clampSideTurns(*proj.HistoryTurns) >= cur.SideHistoryTurns() {
		out = append(out, "side.history_turns")
	}
	if proj.MaxReplyBytes != nil && clampSideReply(*proj.MaxReplyBytes) >= cur.SideMaxReplyBytes() {
		out = append(out, "side.max_reply_bytes")
	}
	return out
}
