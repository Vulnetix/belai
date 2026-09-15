package transcript

// Estimate is the result of hybrid context accounting.
type Estimate struct {
	Tokens         int // UsageTokens + TrailingTokens
	UsageTokens    int // provider-reported total at the anchor
	TrailingTokens int // chars/4 estimate for messages after the anchor
	LastUsageIndex int // index of the anchor, or -1 when there is none
}

// EstimateContext anchors on the last assistant message carrying provider
// usage and estimates only the messages after it. With no anchor, every
// message is estimated and LastUsageIndex is -1.
func EstimateContext(msgs []Message) Estimate {
	anchor := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Usage != nil {
			anchor = i
			break
		}
	}

	e := Estimate{LastUsageIndex: anchor}
	if anchor < 0 {
		for _, m := range msgs {
			e.TrailingTokens += EstimateTokens(m)
		}
		e.Tokens = e.TrailingTokens
		return e
	}

	e.UsageTokens = msgs[anchor].Usage.Total()
	for i := anchor + 1; i < len(msgs); i++ {
		e.TrailingTokens += EstimateTokens(msgs[i])
	}
	e.Tokens = e.UsageTokens + e.TrailingTokens
	return e
}

// Meter renders context pressure for the status bar.
type Meter struct {
	Used     int  // Estimate.Tokens
	Limit    int  // 0 when the model's window is unknown
	Anchored bool // a provider usage report backs Used
	Stale    bool // Used predates a compaction; a percentage would be wrong
}

// PercentRemaining returns the remaining share of the window. ok is false when
// the window is unknown or the meter is stale — the caller then renders "?"
// rather than a number.
func (m Meter) PercentRemaining() (pct int, ok bool) {
	if m.Limit <= 0 || m.Stale {
		return 0, false
	}
	if m.Used >= m.Limit {
		return 0, true
	}
	return int(float64(m.Limit-m.Used) / float64(m.Limit) * 100), true
}
