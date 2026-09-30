package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestTTSDefaults(t *testing.T) {
	var nilSettings *TTSSettings
	for name, s := range map[string]*TTSSettings{"nil": nilSettings, "empty": {}} {
		if s.TTSEnabled() || s.TTSConsented() || s.TTSReadReports() ||
			s.TTSVoiceOr() != DefaultTTSVoice || s.TTSSpeedOr() != 1 || s.TTSCacheMBOr() != DefaultTTSCacheMB {
			t.Errorf("%s is not the documented default", name)
		}
	}
}

func TestTTSNeedsBothEnabledAndConsented(t *testing.T) {
	on, off := true, false
	cases := []struct {
		s    TTSSettings
		want bool
	}{
		{TTSSettings{Enabled: &on, Consented: &on}, true},
		{TTSSettings{Enabled: &on}, false},
		{TTSSettings{Enabled: &on, Consented: &off}, false},
		{TTSSettings{Consented: &on}, false},
		{TTSSettings{Enabled: &off, Consented: &on}, false},
	}
	for i, c := range cases {
		if got := c.s.TTSEnabled(); got != c.want {
			t.Errorf("case %d: TTSEnabled = %v", i, got)
		}
	}
}

func TestTTSMergeKeepsUnsetFields(t *testing.T) {
	on, off := true, false
	speed, mb := 1.5, 64
	a := &TTSSettings{Enabled: &on, Voice: "en-GB-RyanNeural", Speed: &speed}
	a.merge(&TTSSettings{})
	if !*a.Enabled || a.Voice != "en-GB-RyanNeural" || *a.Speed != 1.5 {
		t.Fatalf("an empty merge changed %+v", a)
	}
	a.merge(&TTSSettings{Enabled: &off, Consented: &on, ReadReports: &on, Voice: "en-US-AriaNeural", CacheMB: &mb})
	if *a.Enabled || !*a.Consented || !*a.ReadReports || a.Voice != "en-US-AriaNeural" || *a.CacheMB != 64 || *a.Speed != 1.5 {
		t.Fatalf("merge = %+v", a)
	}
}

func TestValidateTTS(t *testing.T) {
	speed := func(f float64) *float64 { return &f }
	mb := func(n int) *int { return &n }
	bad := map[string]TTSSettings{
		"tts.voice":    {Voice: "a b"},
		"tts.voice ":   {Voice: "x'/><y"},
		"tts.voice  ":  {Voice: strings.Repeat("a", 65)},
		"tts.speed":    {Speed: speed(0.4)},
		"tts.speed ":   {Speed: speed(3.1)},
		"tts.cache_mb": {CacheMB: mb(-1)},
	}
	for want, v := range bad {
		v := v
		err := ValidateTTS(Settings{TTS: &v})
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(want)) {
			t.Errorf("%+v: err = %v, want one naming %s", v, err, want)
		}
	}
	if err := ValidateTTS(Settings{TTS: &TTSSettings{CacheMB: mb(MaxTTSCacheMB + 1)}}); err == nil {
		t.Error("an oversized cache was accepted")
	}
	for _, ok := range []TTSSettings{
		{}, {Voice: "en-US-AndrewMultilingualNeural"}, {Speed: speed(0.5)}, {Speed: speed(3)}, {CacheMB: mb(0)}, {CacheMB: mb(MaxTTSCacheMB)},
	} {
		ok := ok
		if err := ValidateTTS(Settings{TTS: &ok}); err != nil {
			t.Errorf("%+v rejected: %v", ok, err)
		}
	}
	if err := ValidateTTS(Settings{}); err != nil {
		t.Fatal(err)
	}
}

func TestTTSSettingsJSONKeys(t *testing.T) {
	on, speed, mb := true, 1.5, 64
	t.Setenv("BELAI_HOME", t.TempDir())
	if err := SaveGlobal(Settings{TTS: &TTSSettings{Enabled: &on, Consented: &on, ReadReports: &on, Voice: "v", Speed: &speed, CacheMB: &mb}}); err != nil {
		t.Fatal(err)
	}
	p, _ := GlobalSettingsPath()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"enabled"`, `"consented"`, `"read_reports"`, `"voice"`, `"speed"`, `"cache_mb"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("%s missing from %s", k, filepath.Base(p))
		}
	}
}

// docs/tts.md documents every key, its default and its limits.
func TestTTSPageMatchesTheSettings(t *testing.T) {
	doc := docparity.Read(t, "docs/tts.md")
	for _, want := range []string{
		"`enabled`", "`consented`", "`read_reports`", "`voice`", "`speed`", "`cache_mb`",
		DefaultTTSVoice, "from 0.5 to 3", "from 0 to 4096", "`256`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/tts.md does not mention %s", want)
		}
	}
	if TTSSpeedMin != 0.5 || TTSSpeedMax != 3 || MaxTTSCacheMB != 4096 || DefaultTTSCacheMB != 256 {
		t.Fatal("the documented limits drifted from the constants")
	}
}

// The README names the tts key.
func TestReadmeNamesTheTTSKey(t *testing.T) {
	if !strings.Contains(docparity.Read(t, "README.md"), "| `tts` |") {
		t.Fatal("README.md has no tts row")
	}
}

func TestResolveTTSIgnoresTheProjectLayer(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	on, speed, mb := true, 2.0, 4096
	project := Settings{TTS: &TTSSettings{Enabled: &on, Consented: &on, ReadReports: &on, Voice: "en-GB-RyanNeural", Speed: &speed, CacheMB: &mb}}
	if err := SaveProject(workdir, project); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if s := eff.Settings.TTS; s.TTSEnabled() || s.TTSConsented() || s.TTSReadReports() ||
		s.TTSVoiceOr() != DefaultTTSVoice || s.TTSSpeedOr() != 1 || s.TTSCacheMBOr() != DefaultTTSCacheMB {
		t.Fatalf("a project file changed tts: %+v", s)
	}
	if _, ok := eff.Origin["tts"]; ok {
		t.Fatal("the project layer was recorded as the origin of tts")
	}
	if merged := (Settings{}).Override(project); merged.TTS.TTSEnabled() || merged.TTS.TTSConsented() {
		t.Fatal("Override let a project file turn reading aloud on")
	}
}

func TestResolveTTSTakesTheUserLayerAndRejectsBadValues(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	on, speed, mb := true, 1.5, 64
	if err := SaveGlobal(Settings{TTS: &TTSSettings{Enabled: &on, Consented: &on, Voice: "en-GB-RyanNeural", Speed: &speed, CacheMB: &mb}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	s := eff.Settings.TTS
	if !s.TTSEnabled() || s.TTSVoiceOr() != "en-GB-RyanNeural" || s.TTSSpeedOr() != 1.5 || s.TTSCacheMBOr() != 64 {
		t.Fatalf("user layer not applied: %+v", s)
	}
	if eff.Origin["tts"] != SourceGlobal {
		t.Fatalf("origin = %v", eff.Origin["tts"])
	}
	bad := 9.0
	if err := SaveGlobal(Settings{TTS: &TTSSettings{Speed: &bad}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(workdir, func(string) string { return "" }, Settings{}); err == nil || !strings.Contains(err.Error(), "tts.speed") {
		t.Fatalf("a bad speed resolved: %v", err)
	}
}
