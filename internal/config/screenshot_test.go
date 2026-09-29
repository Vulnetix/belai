package config

import "testing"

func ptr(b bool) *bool { return &b }

func TestScreenshotDefaultsOn(t *testing.T) {
	var s Settings
	if !s.ScreenshotEnabled() || !s.ScreenshotDesktopEnabled() {
		t.Fatal("both switches default on")
	}
}

func TestScreenshotOffMeansDesktopOffToo(t *testing.T) {
	s := Settings{Screenshot: &ScreenshotSettings{Enabled: ptr(false)}}
	if s.ScreenshotEnabled() || s.ScreenshotDesktopEnabled() {
		t.Fatal("a disabled tool cannot capture the desktop")
	}
	s = Settings{Screenshot: &ScreenshotSettings{Desktop: ptr(false)}}
	if !s.ScreenshotEnabled() || s.ScreenshotDesktopEnabled() {
		t.Fatal("desktop off leaves page capture on")
	}
}

func TestProjectCanOnlyTurnScreenshotOff(t *testing.T) {
	global := Settings{Screenshot: &ScreenshotSettings{Enabled: ptr(false), Desktop: ptr(false)}}
	merged := global.Override(Settings{Screenshot: &ScreenshotSettings{Enabled: ptr(true), Desktop: ptr(true)}})
	if merged.ScreenshotEnabled() || merged.ScreenshotDesktopEnabled() {
		t.Fatal("a project layer turned the screenshot tool on")
	}
	merged = Settings{}.Override(Settings{Screenshot: &ScreenshotSettings{Desktop: ptr(false)}})
	if merged.ScreenshotDesktopEnabled() {
		t.Fatal("a project layer could not turn desktop capture off")
	}
	merged = Settings{}.Override(Settings{Screenshot: &ScreenshotSettings{Enabled: ptr(false)}})
	if merged.ScreenshotEnabled() {
		t.Fatal("a project layer could not turn the tool off")
	}
}

func TestProjectTrueDoesNotErasePriorFalse(t *testing.T) {
	out := mergeScreenshot(&ScreenshotSettings{Desktop: ptr(false)}, &ScreenshotSettings{Desktop: ptr(true)}, true)
	if out.Desktop == nil || *out.Desktop {
		t.Fatal("project true replaced a user false")
	}
	if got := mergeScreenshot(nil, &ScreenshotSettings{Enabled: ptr(true)}, true); got != nil {
		t.Fatalf("a project true alone must change nothing, got %+v", got)
	}
}

func TestUserLayerMayTurnScreenshotBackOn(t *testing.T) {
	out := mergeScreenshot(&ScreenshotSettings{Enabled: ptr(false)}, &ScreenshotSettings{Enabled: ptr(true)}, false)
	if out.Enabled == nil || !*out.Enabled {
		t.Fatal("a user layer must be able to turn the tool on")
	}
}
