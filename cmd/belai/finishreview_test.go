package main

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestApplyFinishReviewFlag(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k != "BELAI_FINISH_REVIEW" {
				t.Fatalf("read %s", k)
			}
			return v
		}
	}
	cases := []struct {
		name string
		flag bool
		env  string
		on   bool
	}{
		{"nothing set", false, "", true},
		{"flag", true, "", false},
		{"env 0", false, "0", false},
		{"env false", false, "false", false},
		{"env 1 does not switch on a flagged-off gate", true, "1", false},
		{"env 1", false, "1", true},
		{"env noise", false, "banana", true},
	}
	for _, tc := range cases {
		var s config.Settings
		applyFinishReviewFlag(&s, tc.flag, env(tc.env))
		if s.FinishReviewEnabled() != tc.on {
			t.Errorf("%s: enabled = %v, want %v", tc.name, s.FinishReviewEnabled(), tc.on)
		}
	}
	// Other keys of the block survive.
	rounds := 4
	s := config.Settings{FinishReview: &config.FinishReviewSettings{MaxRounds: &rounds}}
	applyFinishReviewFlag(&s, true, env(""))
	if s.FinishReviewEnabled() || s.FinishReviewMaxRounds() != 4 {
		t.Fatalf("flag clobbered the block: %+v", s.FinishReview)
	}
	// With nothing to apply the settings are left alone.
	var untouched config.Settings
	applyFinishReviewFlag(&untouched, false, env(""))
	if untouched.FinishReview != nil {
		t.Fatal("an unset flag created a block")
	}
}
