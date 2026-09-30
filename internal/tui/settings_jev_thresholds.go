package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// jevThresholdRowPrefix marks the /settings slider rows of the Jev score
// cut-offs. The prefix differs from jevRowPrefix so the job toggles stay a
// list of toggles.
const jevThresholdRowPrefix = "jevt:"

const (
	// sliderStep is what left and right move a cut-off by; sliderFine is the
	// shifted step. Both are exact in hundredths, so a value never drifts.
	sliderStep = 0.05
	sliderFine = 0.01
	// sliderCells is the bar's width. Each cell has eight partial fills, so the
	// knob moves in 1/96 of the range and a 0.01 step is always visible.
	sliderCells = 12
)

var jevThresholdLabels = map[string]string{
	"allow_at": "jev allow at", "deny_at": "jev deny at", "route_at": "jev route at",
	"drop_at": "jev drop at", "keep_at": "jev keep at", "strong_at": "jev strong at",
	"swap_at": "jev swap at", "voice_at": "jev voice at", "simple_at": "jev simple at", "triage_at": "jev triage at",
	"hit_at": "jev hit at", "lead_at": "jev lead at",
	"option_hit": "jev option hit", "option_margin": "jev option margin", "option_lead": "jev option lead",
	"mode_confident": "jev mode sure", "mode_margin": "jev mode margin", "mode_headless": "jev mode headless",
}

var jevThresholdHelp = map[string]string{
	"allow_at":       "security gate lets a call through at or below this (max 0.50). Raising it loosens the gate",
	"deny_at":        "security gate blocks at or above this (min 0.50). Lowering it tightens the gate",
	"route_at":       "a routing candidate must score above this to be chosen",
	"drop_at":        "an item scoring below this leaves a tool, skill or result list",
	"keep_at":        "an item scoring at or above this is kept and preloaded",
	"strong_at":      "an item scoring at or above this is added even when nothing else picked it",
	"swap_at":        "a single candidate at or above this replaces the Bash call it rates",
	"voice_at":       "a spoken instruction runs a skill, crew, process, prompt, profile or mode only when exactly one target scores at or above this",
	"simple_at":      "a request scoring at or above this is worked as a simple one: no goal contract, prefetch or test run (min 0.50)",
	"triage_at":      "below this another edit pass is judged unlikely to clear language server errors",
	"hit_at":         "a located file at or above this is a hit",
	"lead_at":        "a located file from here up to the hit level is a lead; below it is dropped",
	"option_hit":     "the first option needs this score to be a clear winner",
	"option_margin":  "and this lead over the second option",
	"option_lead":    "or this lead alone, whatever its score",
	"mode_confident": "a detected intent is confident at this score",
	"mode_margin":    "and this lead over the runner-up",
	"mode_headless":  "a non-interactive run accepts the top intent at this score",
}

// sliderBar draws v in [0,1] as a bar with a partial-fill knob: full blocks up
// to the value, one eighth-block for the remainder, a dotted track after it.
func sliderBar(v float64) string {
	v = math.Max(0, math.Min(1, v))
	eighths := int(math.Round(v * sliderCells * 8))
	full, part := eighths/8, eighths%8
	var b strings.Builder
	b.WriteString(strings.Repeat("█", full))
	used := full
	if part > 0 {
		b.WriteRune([]rune(" ▏▎▍▌▋▊▉")[part])
		used++
	}
	b.WriteString(strings.Repeat("·", sliderCells-used))
	return b.String()
}

// jevThresholdRows builds one slider row per cut-off, in the documented order.
func jevThresholdRows(s config.Settings, origin map[string]config.Source) []settingsRow {
	var th *config.JevThresholdSettings
	if s.Jev != nil {
		th = s.Jev.Thresholds
	}
	keys := config.JevThresholdKeys()
	rows := make([]settingsRow, 0, len(keys))
	for _, k := range keys {
		v, set, _ := th.Value(k)
		src := "default"
		if set {
			src = sourceLabel(origin["jev"])
		}
		rows = append(rows, settingsRow{
			key:   jevThresholdRowPrefix + k,
			label: jevThresholdLabels[k],
			kind:  "slider",
			value: fmt.Sprintf("%s %.2f", sliderBar(v), v),
			src:   src,
			help:  jevThresholdHelp[k] + "  (←/→ 0.05, shift 0.01, x default)",
		})
	}
	return rows
}

// roundHundredths keeps a stepped value exact to two places.
func roundHundredths(v float64) float64 { return math.Round(v*100) / 100 }

// stepJevThreshold moves one cut-off by delta and saves it to the user's
// settings, which is the only layer that reads it. A move that would break a
// rule (the allow and deny cut-offs closing, an ordered pair inverting) or leave
// [0,1] is refused with the rule's message and nothing is written.
func (a *App) stepJevThreshold(key string, delta float64) error {
	var cur *config.JevThresholdSettings
	if a.settings.Jev != nil {
		cur = a.settings.Jev.Thresholds
	}
	v, _, ok := cur.Value(key)
	if !ok {
		return fmt.Errorf("unknown threshold %q", key)
	}
	next := roundHundredths(v + delta)
	if next < 0 || next > 1 {
		return nil // at the end of the track
	}
	cand := cur.With(key, &next)
	if err := cand.Validate(); err != nil {
		return err
	}
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.Jev == nil {
			s.Jev = &config.JevSettings{}
		}
		s.Jev.Thresholds = s.Jev.Thresholds.With(key, &next)
	})
}

// resetJevThreshold returns one cut-off to its default.
func (a *App) resetJevThreshold(key string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.Jev == nil {
			return
		}
		s.Jev.Thresholds = s.Jev.Thresholds.With(key, nil)
	})
}
