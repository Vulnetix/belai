package config

// ScreenshotSettings governs the Screenshot tool (docs/screenshots.md).
// Both switches default on. A repository-visible project layer may turn
// either off and never on: the tool lets the model see the screen or a page,
// so a repository must not be able to enable it for a user who did not.
type ScreenshotSettings struct {
	// Enabled turns the Screenshot tool on or off. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// Desktop allows capturing the whole screen (target "desktop"). nil means
	// on. Every desktop capture asks, whatever this says. Off leaves only
	// loopback page captures.
	Desktop *bool `json:"desktop,omitempty"`
}

// ScreenshotEnabled reports whether the Screenshot tool is on. Default on.
func (s Settings) ScreenshotEnabled() bool {
	return s.Screenshot == nil || s.Screenshot.Enabled == nil || *s.Screenshot.Enabled
}

// ScreenshotDesktopEnabled reports whether whole-screen capture is allowed.
// Default on.
func (s Settings) ScreenshotDesktopEnabled() bool {
	return s.ScreenshotEnabled() && (s.Screenshot == nil || s.Screenshot.Desktop == nil || *s.Screenshot.Desktop)
}

// mergeScreenshot folds in over cur. From the project layer only an explicit
// false takes effect.
func mergeScreenshot(cur, in *ScreenshotSettings, project bool) *ScreenshotSettings {
	if in == nil {
		return cur
	}
	out := &ScreenshotSettings{}
	if cur != nil {
		*out = *cur
	}
	pick := func(dst **bool, v *bool) {
		if v == nil || (project && *v) {
			return
		}
		b := *v
		*dst = &b
	}
	pick(&out.Enabled, in.Enabled)
	pick(&out.Desktop, in.Desktop)
	if out.Enabled == nil && out.Desktop == nil {
		return cur
	}
	return out
}
