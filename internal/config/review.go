package config

import (
	"fmt"
	"strings"
)

// ReviewSettings governs the code review turn, /review and `belai review`
// (docs/review.md). A nil field means the built-in default.
type ReviewSettings struct {
	// Enabled switches the review off. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxDiffKB caps the diff a review reads, in KiB. Default 400, clamped to
	// 16 through 2048. A larger diff is cut and the result says so.
	MaxDiffKB *int `json:"max_diff_kb,omitempty"`
	// Post is whether a finished review may be posted to a pull request as a
	// comment: ask (default, the user confirms each post), deny, or allow
	// (the headless CLI posts without a question when -comment is also given).
	// Only the user's own layers may set allow.
	Post *string `json:"post,omitempty"`
	// RubricFile is the path of the user's own file of extra review criteria,
	// one per line. Only the user's own layers may set it.
	RubricFile *string `json:"rubric_file,omitempty"`
}

// Review post values and diff bounds.
const (
	ReviewPostAsk   = "ask"
	ReviewPostDeny  = "deny"
	ReviewPostAllow = "allow"

	DefaultReviewMaxDiffKB = 400
	MinReviewMaxDiffKB     = 16
	MaxReviewMaxDiffKB     = 2048
)

// ReviewPostValues is the closed set of review.post values.
var ReviewPostValues = []string{ReviewPostAsk, ReviewPostDeny, ReviewPostAllow}

// ReviewEnabled reports whether /review and `belai review` are available.
// Default on.
func (s Settings) ReviewEnabled() bool {
	return s.Review == nil || s.Review.Enabled == nil || *s.Review.Enabled
}

// ReviewMaxDiffBytes is the diff cap in bytes: the set value clamped to its
// bounds, otherwise the default.
func (s Settings) ReviewMaxDiffBytes() int {
	if s.Review == nil || s.Review.MaxDiffKB == nil {
		return DefaultReviewMaxDiffKB << 10
	}
	return clampReviewKB(*s.Review.MaxDiffKB) << 10
}

// ReviewPostMode is how a review may be posted: ask, deny or allow. Default
// ask, and any value this build does not know is deny, so a typo can never
// widen what the harness posts.
func (s Settings) ReviewPostMode() string {
	if s.Review == nil || s.Review.Post == nil {
		return ReviewPostAsk
	}
	switch v := strings.ToLower(strings.TrimSpace(*s.Review.Post)); v {
	case ReviewPostAsk, ReviewPostAllow:
		return v
	}
	return ReviewPostDeny
}

// ReviewRubricFile is the path of the user's extra criteria file, or "".
func (s Settings) ReviewRubricFile() string {
	if s.Review == nil || s.Review.RubricFile == nil {
		return ""
	}
	return strings.TrimSpace(*s.Review.RubricFile)
}

func clampReviewKB(n int) int {
	return min(max(n, MinReviewMaxDiffKB), MaxReviewMaxDiffKB)
}

// ValidateReview checks review.post and the rubric file path.
func ValidateReview(s Settings) error {
	r := s.Review
	if r == nil {
		return nil
	}
	if r.Post != nil {
		v := strings.ToLower(strings.TrimSpace(*r.Post))
		ok := false
		for _, n := range ReviewPostValues {
			ok = ok || v == n
		}
		if !ok {
			return fmt.Errorf("review.post must be one of ask, deny or allow, got %q", *r.Post)
		}
	}
	if r.RubricFile != nil {
		p := strings.TrimSpace(*r.RubricFile)
		if len(p) > 4096 || strings.ContainsAny(p, "\x00\r\n") {
			return fmt.Errorf("review.rubric_file is not a usable path")
		}
	}
	return nil
}

// mergeReview folds from over base. A user layer sets any key. The project
// layer, which a cloned repository controls, may turn the review off, lower the
// diff cap and set post to deny; it never enables the review, raises the cap,
// allows posting or names a rubric file.
func mergeReview(base, from *ReviewSettings, project bool) *ReviewSettings {
	if from == nil {
		return base
	}
	out := &ReviewSettings{}
	if base != nil {
		*out = *base
	}
	if from.Enabled != nil && (!project || !*from.Enabled) {
		v := *from.Enabled
		out.Enabled = &v
	}
	cur := Settings{Review: out}
	if from.MaxDiffKB != nil {
		v := clampReviewKB(*from.MaxDiffKB)
		if !project || v < cur.ReviewMaxDiffBytes()>>10 {
			out.MaxDiffKB = &v
		}
	}
	if from.Post != nil {
		v := strings.ToLower(strings.TrimSpace(*from.Post))
		if !project || v == ReviewPostDeny {
			out.Post = &v
		}
	}
	if from.RubricFile != nil && !project {
		v := strings.TrimSpace(*from.RubricFile)
		out.RubricFile = &v
	}
	return out
}

// reviewIgnored names the project keys mergeReview would not apply on top of
// user, for the note Resolve prints.
func reviewIgnored(user, proj *ReviewSettings) []string {
	if proj == nil {
		return nil
	}
	var out []string
	cur := Settings{Review: user}
	if proj.Enabled != nil && *proj.Enabled && !cur.ReviewEnabled() {
		out = append(out, "review.enabled")
	}
	if proj.MaxDiffKB != nil && clampReviewKB(*proj.MaxDiffKB) >= cur.ReviewMaxDiffBytes()>>10 {
		out = append(out, "review.max_diff_kb")
	}
	if proj.Post != nil && strings.ToLower(strings.TrimSpace(*proj.Post)) != ReviewPostDeny {
		out = append(out, "review.post")
	}
	if proj.RubricFile != nil {
		out = append(out, "review.rubric_file")
	}
	return out
}
