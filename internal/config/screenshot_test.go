package config

import (
	"encoding/json"
	"strings"
	"testing"
)

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

func TestProviderModelImagesRoundTrips(t *testing.T) {
	var p ProviderProfile
	if err := json.Unmarshal([]byte(`{"base_url":"http://x","api":"openai-chat","models":[{"id":"a","images":true},{"id":"b","images":false},{"id":"c"}]}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Models[0].Images == nil || !*p.Models[0].Images || p.Models[1].Images == nil || *p.Models[1].Images || p.Models[2].Images != nil {
		t.Fatalf("models = %+v", p.Models)
	}
	out, _ := json.Marshal(p.Models[2])
	if strings.Contains(string(out), "images") {
		t.Fatalf("an undeclared model must not write the key: %s", out)
	}
}

func TestClipboardImagesDefaultsOnAndProjectCanOnlyTurnItOff(t *testing.T) {
	var s Settings
	if !s.ClipboardImagesEnabled() {
		t.Fatal("default on")
	}
	off := Settings{UI: &UISettings{ClipboardImages: ptr(false)}}
	if off.ClipboardImagesEnabled() {
		t.Fatal("off was ignored")
	}
	// A project file cannot turn it back on over a user's off.
	merged := off.Override(Settings{UI: &UISettings{ClipboardImages: ptr(true)}})
	if merged.ClipboardImagesEnabled() {
		t.Fatal("a project layer turned clipboard images on")
	}
	// It can turn it off, and its other UI keys still apply.
	merged = Settings{}.Override(Settings{UI: &UISettings{ClipboardImages: ptr(false), Colors: ptr(false)}})
	if merged.ClipboardImagesEnabled() || merged.ColorsEnabled() {
		t.Fatalf("project off should apply: %+v", merged.UI)
	}
	// The same rule through Resolve's layer fold.
	e := &Effective{Settings: Settings{UI: &UISettings{ClipboardImages: ptr(false)}}, Origin: map[string]Source{}}
	e.apply(Settings{UI: &UISettings{ClipboardImages: ptr(true)}}, SourceProject)
	if e.Settings.ClipboardImagesEnabled() {
		t.Fatal("Resolve let a project layer turn clipboard images on")
	}
	e.apply(Settings{UI: &UISettings{ClipboardImages: ptr(true)}}, SourceGlobal)
	if !e.Settings.ClipboardImagesEnabled() {
		t.Fatal("a user layer must be able to turn it on")
	}
}
