package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestVoiceDefaults(t *testing.T) {
	var s *VoiceSettings
	if s.VoiceEnabled() || s.VoiceModeOr() != VoiceModePushToTalk || s.VoiceDeliveryOr() != VoiceDeliveryInsert ||
		!s.VoiceCleanupEnabled() || s.VoiceKeyOr() != DefaultVoiceKey || s.VoiceDevice() != "" {
		t.Fatal("nil VoiceSettings is not the documented default")
	}
	empty := &VoiceSettings{}
	if empty.VoiceEnabled() || empty.VoiceModeOr() != "push_to_talk" || empty.VoiceDeliveryOr() != "insert" ||
		!empty.VoiceCleanupEnabled() || empty.VoiceKeyOr() != "f11" {
		t.Fatal("empty VoiceSettings is not the documented default")
	}
	off := false
	if (&VoiceSettings{Cleanup: &off}).VoiceCleanupEnabled() {
		t.Fatal("cleanup: false was ignored")
	}
}

func TestVoiceMergeKeepsUnsetFields(t *testing.T) {
	on, off := true, false
	a := &VoiceSettings{Enabled: &on, Mode: VoiceModeListen, Key: "f13", Device: "hw:1,0"}
	a.merge(&VoiceSettings{Delivery: VoiceDeliverySubmit, Cleanup: &off})
	if !a.VoiceEnabled() || a.Mode != VoiceModeListen || a.Key != "f13" || a.Device != "hw:1,0" ||
		a.Delivery != VoiceDeliverySubmit || a.VoiceCleanupEnabled() {
		t.Fatalf("merge = %+v", a)
	}
	a.merge(&VoiceSettings{Enabled: &off, Mode: VoiceModePushToTalk, Key: "f11", Device: "default"})
	if a.VoiceEnabled() || a.Mode != VoiceModePushToTalk || a.Key != "f11" || a.Device != "default" {
		t.Fatalf("second merge = %+v", a)
	}
}

// Voice is a per-user preference: the project layer is dropped.
func TestResolveVoiceIgnoresProject(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	on := true
	if err := SaveProject(workdir, Settings{Voice: &VoiceSettings{Enabled: &on, Mode: VoiceModeListen, Delivery: VoiceDeliverySubmit, Device: "x"}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if v := eff.Settings.Voice; v.VoiceEnabled() || v.VoiceModeOr() != VoiceModePushToTalk || v.VoiceDeliveryOr() != VoiceDeliveryInsert || v.VoiceDevice() != "" {
		t.Fatalf("a project file changed voice: %+v", v)
	}
	if err := SaveGlobal(Settings{Voice: &VoiceSettings{Enabled: &on, Mode: VoiceModeListen, Delivery: VoiceDeliverySubmit, Key: "ctrl+space", Device: "hw:1,0"}}); err != nil {
		t.Fatal(err)
	}
	eff, err = Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	v := eff.Settings.Voice
	if !v.VoiceEnabled() || v.VoiceModeOr() != VoiceModeListen || v.VoiceDeliveryOr() != VoiceDeliverySubmit ||
		v.VoiceKeyOr() != "ctrl+space" || v.VoiceDevice() != "hw:1,0" {
		t.Fatalf("global voice = %+v", v)
	}
	if eff.Origin["voice"] != SourceGlobal {
		t.Fatalf("origin = %v", eff.Origin["voice"])
	}
	if merged := (Settings{}).Override(Settings{Voice: &VoiceSettings{Enabled: &on}}); merged.Voice.VoiceEnabled() {
		t.Fatal("Override let a project file turn voice on")
	}
}

func TestValidateVoice(t *testing.T) {
	good := []VoiceSettings{
		{},
		{Mode: "listen", Delivery: "submit", Key: "f16", Device: "alsa_input.usb-0.analog-stereo"},
		{Key: "ctrl+space", Device: "plughw:CARD=PCH,DEV=0"},
	}
	for _, v := range good {
		v := v
		if err := ValidateVoice(Settings{Voice: &v}); err != nil {
			t.Errorf("%+v rejected: %v", v, err)
		}
	}
	if err := ValidateVoice(Settings{}); err != nil {
		t.Errorf("no voice block rejected: %v", err)
	}
	bad := map[string]VoiceSettings{
		"voice.mode":     {Mode: "always"},
		"voice.delivery": {Delivery: "send"},
		"voice.key":      {Key: "f12"},
		"voice.device":   {Device: "--device=x"},
	}
	for want, v := range bad {
		v := v
		err := ValidateVoice(Settings{Voice: &v})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: err = %v, want it to name %s", v, err, want)
		}
	}
	for _, d := range []string{"a b", "a;b", "$(x)", "a\nb", "-f", strings.Repeat("a", 200)} {
		if err := ValidateVoice(Settings{Voice: &VoiceSettings{Device: d}}); err == nil {
			t.Errorf("device %q accepted", d)
		}
	}
}

// An invalid block in the user's settings fails resolution with the key named.
func TestResolveRejectsInvalidVoice(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	if err := SaveGlobal(Settings{Voice: &VoiceSettings{Mode: "sometimes"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(t.TempDir(), func(string) string { return "" }, Settings{}); err == nil || !strings.Contains(err.Error(), "voice.mode") {
		t.Fatalf("err = %v", err)
	}
}

// The settings JSON carries the documented keys and nothing else.
func TestVoiceSettingsJSONKeys(t *testing.T) {
	on := true
	dir := t.TempDir()
	t.Setenv("BELAI_HOME", dir)
	if err := SaveGlobal(Settings{Voice: &VoiceSettings{Enabled: &on, Mode: "listen", Delivery: "submit", Cleanup: &on, Key: "f11", Device: "d"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := GlobalSettingsPath()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"enabled"`, `"mode"`, `"delivery"`, `"cleanup"`, `"key"`, `"device"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("%s missing from %s", k, filepath.Base(p))
		}
	}
}

// docs/voice.md documents every key, every value and the defaults.
func TestVoicePageMatchesTheSettings(t *testing.T) {
	doc := docparity.Read(t, "docs/voice.md")
	for _, want := range []string{
		"`enabled`", "`mode`", "`delivery`", "`cleanup`", "`key`", "`device`",
		VoiceModePushToTalk, VoiceModeListen, VoiceDeliveryInsert, VoiceDeliverySubmit, DefaultVoiceKey,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/voice.md does not mention %s", want)
		}
	}
	for _, k := range VoiceKeys {
		if !strings.Contains(doc, k) {
			t.Errorf("docs/voice.md does not list the key %s", k)
		}
	}
}

func TestVoiceEnabledOrFollowsTheDefaultUnlessSet(t *testing.T) {
	on, off := true, false
	var nilSettings *VoiceSettings
	for name, tc := range map[string]struct {
		s    *VoiceSettings
		def  bool
		want bool
	}{
		"nil, built in":         {nilSettings, true, true},
		"nil, not built in":     {nilSettings, false, false},
		"unset, built in":       {&VoiceSettings{}, true, true},
		"unset, not built in":   {&VoiceSettings{}, false, false},
		"explicit off wins":     {&VoiceSettings{Enabled: &off}, true, false},
		"explicit on wins":      {&VoiceSettings{Enabled: &on}, false, true},
		"explicit on, built in": {&VoiceSettings{Enabled: &on}, true, true},
	} {
		if got := tc.s.VoiceEnabledOr(tc.def); got != tc.want {
			t.Errorf("%s: VoiceEnabledOr(%v) = %v, want %v", name, tc.def, got, tc.want)
		}
	}
	// The plain accessor still means "explicitly on".
	if (&VoiceSettings{}).VoiceEnabled() {
		t.Error("VoiceEnabled reports on for an unset setting")
	}
}

func TestVoiceKeysAreUsableOnACommonKeyboard(t *testing.T) {
	has := func(k string) bool {
		for _, v := range VoiceKeys {
			if v == k {
				return true
			}
		}
		return false
	}
	for _, k := range []string{"f11", "ctrl+space", "ctrl+]", "ctrl+g"} {
		if !has(k) {
			t.Errorf("VoiceKeys lacks %s", k)
		}
	}
	if VoiceKeys[0] != DefaultVoiceKey || VoiceKeys[1] != "ctrl+space" {
		t.Fatalf("VoiceKeys starts %v: the default and the fallback for a terminal that keeps it come first", VoiceKeys[:2])
	}
	for _, k := range VoiceKeys {
		if err := ValidateVoice(Settings{Voice: &VoiceSettings{Key: k}}); err != nil {
			t.Errorf("%s is listed but rejected: %v", k, err)
		}
	}
}
