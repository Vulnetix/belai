package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeGlobal puts a settings.json under a fresh BELAI_HOME and returns the
// working directory a project file can go under.
func writeGlobal(t *testing.T, body string) string {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	path, err := GlobalSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return t.TempDir()
}

func writeProject(t *testing.T, workdir, body string) {
	t.Helper()
	path := ProjectSettingsPath(workdir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noEnv(string) string { return "" }

// TestVulnetixBlockReachesTheEffectiveSettings pins the review keys a user
// writes in their own settings.json: they must arrive in the settings the TUI
// runs with, not only sit in the file.
func TestVulnetixBlockReachesTheEffectiveSettings(t *testing.T) {
	wd := writeGlobal(t, `{"vulnetix":{"autofix":true,"subcommands":["sca","sast"],"timeout":"10m"}}`)
	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	v := eff.Settings.Vulnetix
	if v == nil {
		t.Fatal("the vulnetix block did not reach the effective settings")
	}
	if !v.AutoFixEnabled() {
		t.Error("vulnetix.autofix was dropped")
	}
	if !slices.Equal(v.Subcommands, []string{"sca", "sast"}) {
		t.Errorf("vulnetix.subcommands = %v", v.Subcommands)
	}
	if v.ReviewTimeout() != 10*time.Minute {
		t.Errorf("vulnetix.timeout = %v", v.ReviewTimeout())
	}
}

// A repository's own settings file may switch the unattended fix off, never
// on, and may not narrow the scanners or set the timeout: those are the user's.
func TestProjectLayerCannotLoosenTheReview(t *testing.T) {
	wd := writeGlobal(t, `{"vulnetix":{"subcommands":["sca"],"timeout":"5m"}}`)
	writeProject(t, wd, `{"vulnetix":{"autofix":true,"subcommands":["sast"],"timeout":"1s"}}`)

	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	v := eff.Settings.Vulnetix
	if v.AutoFixEnabled() {
		t.Error("a project file turned autofix on")
	}
	if !slices.Equal(v.Subcommands, []string{"sca"}) || v.ReviewTimeout() != 5*time.Minute {
		t.Errorf("a project file changed the scan set or timeout: %v %v", v.Subcommands, v.ReviewTimeout())
	}

	merged, err := LoadMerged(wd)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Vulnetix.AutoFixEnabled() || !slices.Equal(merged.Vulnetix.Subcommands, []string{"sca"}) {
		t.Errorf("LoadMerged let the project loosen the review: %+v", merged.Vulnetix)
	}
}

func TestProjectLayerMayTurnAutofixOff(t *testing.T) {
	wd := writeGlobal(t, `{"vulnetix":{"autofix":true}}`)
	writeProject(t, wd, `{"vulnetix":{"autofix":false}}`)

	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.Vulnetix.AutoFixEnabled() {
		t.Error("a project file could not turn autofix off")
	}
	if eff.Origin["vulnetix.autofix"] != SourceProject {
		t.Errorf("origin = %q, want project", eff.Origin["vulnetix.autofix"])
	}
	merged, err := LoadMerged(wd)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Vulnetix.AutoFixEnabled() {
		t.Error("LoadMerged ignored the project's autofix off")
	}
}

func TestVulnetixReviewDefaults(t *testing.T) {
	var none *VulnetixSettings
	if none.ReviewTimeout() != 0 || none.ReviewSubcommands() != nil {
		t.Error("an absent vulnetix block must mean no timeout and every scanner")
	}
	wd := writeGlobal(t, `{}`)
	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.Vulnetix.AutoFixEnabled() || eff.Settings.Vulnetix.ReviewTimeout() != 0 {
		t.Error("autofix must default off and the review timeout to none")
	}
}

func TestValidateVulnetixNamesTheKey(t *testing.T) {
	good := []Settings{
		{},
		{Vulnetix: &VulnetixSettings{}},
		{Vulnetix: &VulnetixSettings{Subcommands: VulnetixSubcommandNames, Timeout: "90m"}},
		{Vulnetix: &VulnetixSettings{Timeout: "24h"}},
	}
	for _, s := range good {
		if err := ValidateVulnetix(s); err != nil {
			t.Errorf("%+v refused: %v", s.Vulnetix, err)
		}
	}
	bad := map[string]Settings{
		"vulnetix.subcommands": {Vulnetix: &VulnetixSettings{Subcommands: []string{"sca", "rm -rf"}}},
		"vulnetix.timeout":     {Vulnetix: &VulnetixSettings{Timeout: "soon"}},
	}
	for key, s := range bad {
		err := ValidateVulnetix(s)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v, want one naming the key", key, err)
		}
	}
	for _, d := range []string{"0s", "-5m", "25h"} {
		if err := ValidateVulnetix(Settings{Vulnetix: &VulnetixSettings{Timeout: d}}); err == nil {
			t.Errorf("timeout %q accepted", d)
		}
	}
}

func TestResolveRefusesABadVulnetixBlock(t *testing.T) {
	for _, body := range []string{
		`{"vulnetix":{"subcommands":["nmap"]}}`,
		`{"vulnetix":{"timeout":"forever"}}`,
	} {
		wd := writeGlobal(t, body)
		if _, err := Resolve(wd, noEnv, Settings{}); err == nil {
			t.Errorf("%s resolved", body)
		}
	}
}

func TestSweepSettingsReachTheEffectiveSettings(t *testing.T) {
	wd := writeGlobal(t, `{"vulnetix_sweep_enabled":false,"vulnetix_sweep_roots":["/srv/code","/home/me/work"]}`)
	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.SweepEnabled() {
		t.Error("vulnetix_sweep_enabled false was dropped")
	}
	if !slices.Equal(eff.Settings.SweepRoots(), []string{"/srv/code", "/home/me/work"}) {
		t.Errorf("vulnetix_sweep_roots = %v", eff.Settings.SweepRoots())
	}
}

func TestSweepDefaultsOnWithNoRoots(t *testing.T) {
	wd := writeGlobal(t, `{}`)
	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Settings.SweepEnabled() || eff.Settings.SweepRoots() != nil {
		t.Errorf("defaults = enabled %v, roots %v; want on and none", eff.Settings.SweepEnabled(), eff.Settings.SweepRoots())
	}
}

// A repository may switch the sweep off but may not choose where it walks.
func TestProjectLayerMayTurnTheSweepOffButNotPickRoots(t *testing.T) {
	wd := writeGlobal(t, `{"vulnetix_sweep_roots":["/srv/code"]}`)
	writeProject(t, wd, `{"vulnetix_sweep_enabled":false,"vulnetix_sweep_roots":["/etc"]}`)

	eff, err := Resolve(wd, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.SweepEnabled() {
		t.Error("a project file could not turn the sweep off")
	}
	if !slices.Equal(eff.Settings.SweepRoots(), []string{"/srv/code"}) {
		t.Errorf("a project file changed the sweep roots: %v", eff.Settings.SweepRoots())
	}
	merged, err := LoadMerged(wd)
	if err != nil {
		t.Fatal(err)
	}
	if merged.SweepEnabled() || !slices.Equal(merged.SweepRoots(), []string{"/srv/code"}) {
		t.Errorf("LoadMerged: enabled %v roots %v", merged.SweepEnabled(), merged.SweepRoots())
	}

	// A project file turning it on does nothing a default did not.
	wd2 := writeGlobal(t, `{"vulnetix_sweep_enabled":false}`)
	writeProject(t, wd2, `{"vulnetix_sweep_enabled":true}`)
	eff2, err := Resolve(wd2, noEnv, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff2.Settings.SweepEnabled() {
		t.Error("a project file turned the user's sweep switch back on")
	}
}

func TestSweepRootsMustBeAbsolute(t *testing.T) {
	for _, root := range []string{"", "code", "../up", "~/code"} {
		if err := ValidateVulnetix(Settings{VulnetixSweepRoots: []string{root}}); err == nil {
			t.Errorf("sweep root %q accepted", root)
		}
	}
	if err := ValidateVulnetix(Settings{VulnetixSweepRoots: []string{"/srv/code"}}); err != nil {
		t.Error(err)
	}
}

// TestEveryTopLevelSettingReachesTheEffectiveSettings is the guard against a
// key that validates and saves but never takes effect. It fills each top-level
// setting with a non-zero value and applies it as the user's global layer; the
// effective settings must then hold it. The exceptions are deliberate and each
// says why.
func TestEveryTopLevelSettingReachesTheEffectiveSettings(t *testing.T) {
	exempt := map[string]string{
		"BashReadOnly":              "a deprecated alias folded into ReadOnly",
		"AllowProjectWorkspaceDirs": "read from the global layer by Resolve itself, before the project layer is merged",
	}
	rt := reflect.TypeOf(Settings{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		var in Settings
		fillNonZero(reflect.ValueOf(&in).Elem().Field(i))
		eff := Effective{Settings: Settings{}, Origin: map[string]Source{}}
		eff.apply(in, SourceGlobal)
		if reflect.ValueOf(eff.Settings).Field(i).IsZero() {
			if why, ok := exempt[f.Name]; ok {
				t.Logf("%s is not merged: %s", f.Name, why)
				continue
			}
			t.Errorf("the setting %s (%s) is accepted but never reaches the effective settings", f.Name, f.Tag.Get("json"))
		}
	}
}

// fillNonZero sets v and everything under it to a non-zero value.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Float64:
		v.SetFloat(0.5)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillNonZero(v.Elem())
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillNonZero(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillNonZero(k)
		e := reflect.New(v.Type().Elem()).Elem()
		fillNonZero(e)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillNonZero(v.Field(i))
			}
		}
	}
}
