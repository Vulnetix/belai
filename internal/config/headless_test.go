package config

import (
	"strings"
	"testing"
)

func TestHeadlessDefaults(t *testing.T) {
	s := Settings{}
	if s.HeadlessOutputFormat() != "text" || s.HeadlessStdinLimit() != 1<<20 || s.HeadlessEventCap() != 4<<10 {
		t.Fatalf("defaults = %q %d %d", s.HeadlessOutputFormat(), s.HeadlessStdinLimit(), s.HeadlessEventCap())
	}
	s = Settings{Headless: &HeadlessSettings{OutputFormat: "stream-json", MaxStdinBytes: ip(2048), EventTextCap: ip(512)}}
	if s.HeadlessOutputFormat() != "stream-json" || s.HeadlessStdinLimit() != 2048 || s.HeadlessEventCap() != 512 {
		t.Fatalf("overrides = %q %d %d", s.HeadlessOutputFormat(), s.HeadlessStdinLimit(), s.HeadlessEventCap())
	}
}

func TestValidateHeadlessNamesTheKey(t *testing.T) {
	for key, s := range map[string]Settings{
		"headless.output_format":   {Headless: &HeadlessSettings{OutputFormat: "xml"}},
		"headless.max_stdin_bytes": {Headless: &HeadlessSettings{MaxStdinBytes: ip(MaxHeadlessStdinBytes + 1)}},
		"headless.event_text_cap":  {Headless: &HeadlessSettings{EventTextCap: ip(MinHeadlessEventTextCap - 1)}},
	} {
		if err := ValidateHeadless(s); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
	if err := ValidateHeadless(Settings{Headless: &HeadlessSettings{OutputFormat: "json", MaxStdinBytes: ip(MinHeadlessStdinBytes), EventTextCap: ip(MaxHeadlessEventTextCap)}}); err != nil {
		t.Errorf("bounds are inclusive: %v", err)
	}
	if err := ValidateHeadless(Settings{}); err != nil {
		t.Errorf("no block: %v", err)
	}
}

// TestHeadlessIsPerUser pins the rule docs/headless.md states: a repository
// cannot change what a script that runs Belai receives, or raise the caps.
func TestHeadlessIsPerUser(t *testing.T) {
	wd := writeGlobal(t, `{"headless":{"max_stdin_bytes":2048}}`)
	writeProject(t, wd, `{"headless":{"output_format":"stream-json","max_stdin_bytes":16777216,"event_text_cap":1048576}}`)
	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	s := eff.Settings
	if s.HeadlessOutputFormat() != "text" || s.HeadlessStdinLimit() != 2048 || s.HeadlessEventCap() != DefaultHeadlessEventTextCap {
		t.Fatalf("the project layer changed the contract: %q %d %d", s.HeadlessOutputFormat(), s.HeadlessStdinLimit(), s.HeadlessEventCap())
	}
	if eff.Origin["headless"] == SourceProject {
		t.Fatal("headless origin must not be the project layer")
	}
	noted := false
	for _, n := range eff.Notes {
		noted = noted || strings.Contains(n, "headless")
	}
	if !noted {
		t.Errorf("no note says the project headless block was ignored: %v", eff.Notes)
	}
	merged := Settings{Headless: &HeadlessSettings{OutputFormat: "json"}}.Override(Settings{Headless: &HeadlessSettings{OutputFormat: "stream-json"}})
	if merged.HeadlessOutputFormat() != "json" {
		t.Fatalf("Override let the project change the format to %q", merged.HeadlessOutputFormat())
	}
}

func TestHeadlessGlobalOutOfRangeFailsResolve(t *testing.T) {
	wd := writeGlobal(t, `{"headless":{"event_text_cap":5}}`)
	if _, err := Resolve(wd, noEnv, Settings{}); err == nil || !strings.Contains(err.Error(), "headless.event_text_cap") {
		t.Fatalf("err = %v", err)
	}
}
