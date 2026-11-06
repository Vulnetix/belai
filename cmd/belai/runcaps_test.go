package main

import (
	"context"
	"math"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
)

// R1: the flags set the caps over the file, and an unset flag leaves it.
func TestRunCapsRule1_FlagsSetTheCaps(t *testing.T) {
	var s config.Settings
	applyRunCapFlags(&s, 0, 0, 0)
	if s.RunCaps != nil {
		t.Fatal("no flag made a block")
	}
	tokens := 100
	s.RunCaps = &config.RunCapsSettings{MaxTokens: &tokens}
	applyRunCapFlags(&s, 2.5, 0, 45)
	if s.RunCapCostMicroUSD() != 2_500_000 || s.RunCapTokens() != 100 || s.RunCapWallMinutes() != 45 {
		t.Fatalf("caps %+v", s.RunCaps)
	}
	applyRunCapFlags(&s, 0, 9000, 0)
	if s.RunCapTokens() != 9000 || s.RunCapCostMicroUSD() != 2_500_000 {
		t.Fatalf("a flag did not raise the file's value: %+v", s.RunCaps)
	}
	if tokens != 100 {
		t.Fatal("the flag wrote through the settings file's value")
	}
}

// R11: a malformed flag is refused before any run starts.
func TestRunCapsRule11_MalformedFlagsAreRefused(t *testing.T) {
	for _, c := range []struct {
		cost   float64
		tokens int
		wall   int
	}{{-1, 0, 0}, {math.NaN(), 0, 0}, {math.Inf(1), 0, 0}, {0, -5, 0}, {0, 0, -1}, {0, 0, 99999999}} {
		var s config.Settings
		applyRunCapFlags(&s, c.cost, c.tokens, c.wall)
		if err := config.ValidateRunCaps(s); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

// R13: the exit code of a run stopped by a cap is not any other code the
// command uses.
func TestRunCapsRule13_ExitCodeIsDistinct(t *testing.T) {
	for _, other := range []int{0, 1, 2, 130} {
		if exitRunCap == other {
			t.Fatalf("exit code %d is already taken", other)
		}
	}
	if errRunCapReached.Error() == "" {
		t.Fatal("no message")
	}
}

// R11: a session with a malformed cap in the settings does not build.
func TestRunCapsRule11_SessionRefusesMalformedCaps(t *testing.T) {
	neg := -1
	s := config.Settings{RunCaps: &config.RunCapsSettings{MaxTokens: &neg}}
	if rcRunCaps(s, "sess") != nil {
		t.Fatal("a malformed cap built a meter")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	sess, err := newCLISessionLinked(context.Background(), run.Config{}, nil, posture.Defaults(), t.TempDir(), s, false, "sess", false, false, nil, nil)
	if err == nil || sess != nil {
		t.Fatalf("a session was built with a malformed cap: %v", err)
	}
}
