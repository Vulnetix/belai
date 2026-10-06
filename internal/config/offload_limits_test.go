package config

import "testing"

func offloadWith(threshold, preview *int) Settings {
	return Settings{Offload: &OffloadSettings{ThresholdTokens: threshold, PreviewTokens: preview}}
}

func ip(n int) *int { return &n }

// TestOffloadLimitsFollowTheRulesTheDocsState pins the three rules
// docs/context-offload.md gives for offload.threshold_tokens and
// offload.preview_tokens.
func TestOffloadLimitsFollowTheRulesTheDocsState(t *testing.T) {
	cases := []struct {
		name              string
		s                 Settings
		wantThr, wantPrev int
	}{
		{"unset takes the defaults", Settings{}, DefaultOffloadThresholdTokens, DefaultOffloadPreviewTokens},
		{"an empty block takes the defaults", offloadWith(nil, nil), DefaultOffloadThresholdTokens, DefaultOffloadPreviewTokens},
		{"zero is unset", offloadWith(ip(0), ip(0)), DefaultOffloadThresholdTokens, DefaultOffloadPreviewTokens},
		{"negative is unset", offloadWith(ip(-5), ip(-5)), DefaultOffloadThresholdTokens, DefaultOffloadPreviewTokens},
		{"values above the floor stand", offloadWith(ip(9000), ip(2500)), 9000, 2500},
		{"a preview under 200 is raised to 200", offloadWith(ip(9000), ip(50)), 9000, 200},
		{"a threshold at the preview is doubled", offloadWith(ip(1500), ip(1500)), 3000, 1500},
		{"a threshold under the preview is doubled", offloadWith(ip(100), ip(1500)), 3000, 1500},
		{"the floor applies before the threshold rule", offloadWith(ip(150), ip(50)), 400, 200},
	}
	for _, c := range cases {
		thr, prev := c.s.OffloadLimits()
		if thr != c.wantThr || prev != c.wantPrev {
			t.Errorf("%s: threshold %d preview %d, want %d and %d", c.name, thr, prev, c.wantThr, c.wantPrev)
		}
		if thr <= prev {
			t.Errorf("%s: threshold %d is not above preview %d", c.name, thr, prev)
		}
	}
}

func TestOffloadDefaultsOnAndAnySettingsLayerMaySetIt(t *testing.T) {
	var s Settings
	if !s.OffloadEnabled() {
		t.Fatal("offload must default on")
	}
	off := false
	if (Settings{Offload: &OffloadSettings{Enabled: &off}}).OffloadEnabled() {
		t.Fatal("offload.enabled false was ignored")
	}
	// A project file may set it either way: offload changes how much admitted
	// content rides on a request, never what is admitted.
	wd := writeGlobal(t, `{}`)
	writeProject(t, wd, `{"offload":{"enabled":false,"threshold_tokens":9000,"preview_tokens":2500}}`)
	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	thr, prev := eff.Settings.OffloadLimits()
	if eff.Settings.OffloadEnabled() || thr != 9000 || prev != 2500 {
		t.Errorf("project layer: enabled %v threshold %d preview %d", eff.Settings.OffloadEnabled(), thr, prev)
	}
}
