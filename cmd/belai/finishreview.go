package main

import (
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// applyFinishReviewFlag switches the finish review gate off (docs/finish-review.md)
// when -no-finish-review is set or BELAI_FINISH_REVIEW is a false value (0,
// false). Both are the user's own choice, as a settings entry in their own
// layers is; neither can switch the gate on, and a value that is not a boolean
// is ignored. A repository setting cannot do either: the project layer is
// merged before this runs and cannot disable the gate.
func applyFinishReviewFlag(s *config.Settings, flagOff bool, getenv func(string) string) {
	off := flagOff
	if v, err := strconv.ParseBool(strings.TrimSpace(getenv("BELAI_FINISH_REVIEW"))); err == nil && !v {
		off = true
	}
	if !off {
		return
	}
	f := config.FinishReviewSettings{}
	if s.FinishReview != nil {
		f = *s.FinishReview
	}
	no := false
	f.Enabled = &no
	s.FinishReview = &f
}
