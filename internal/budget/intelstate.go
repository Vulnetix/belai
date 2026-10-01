package budget

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sess "github.com/vulnetix/belai/internal/session"
)

// EntryTypeIntelState is the session entry type carrying a snapshot of the
// session intelligence the host computed. It rides the ordinary session sync,
// so the website can draw the limit bars, pace and runway without the host
// having a second channel to it.
const EntryTypeIntelState = "intel_state"

// intelStateVersion is the snapshot's schema version.
const intelStateVersion = 1

// maxIntelStateRoles caps the roles a snapshot lists.
const maxIntelStateRoles = 8

// IntelStateLimit is one plan-limit reading as the website draws it. Times are
// unix milliseconds. Like PlanLimit it holds numbers and identifiers only.
type IntelStateLimit struct {
	Provider   string  `json:"provider"`
	Window     string  `json:"window"`
	Used       float64 `json:"used"`
	ResetsAt   int64   `json:"resetsAt"`
	ObservedAt int64   `json:"observedAt"`
}

// IntelStatePace is how fast tokens are being spent.
type IntelStatePace struct {
	Label         string  `json:"label"`
	TokensPerHour float64 `json:"tokensPerHour"`
	PctPerHour    float64 `json:"pctPerHour"`
}

// IntelStateTrend compares today with the previous seven days.
type IntelStateTrend struct {
	Label string  `json:"label"`
	Ratio float64 `json:"ratio"`
}

// IntelStateRunway says whether the allowance lasts to the end of its window.
type IntelStateRunway struct {
	Kind         string `json:"kind"`
	Lasts        bool   `json:"lasts"`
	UntilSeconds int64  `json:"untilSeconds"`
	ResetSeconds int64  `json:"resetSeconds"`
}

// IntelStateRole is one usage role's tokens in this process.
type IntelStateRole struct {
	Role   string `json:"role"`
	Tokens int64  `json:"tokens"`
}

// IntelState is the intel_state entry's content.
type IntelState struct {
	Version  int               `json:"version"`
	Provider string            `json:"provider"`
	At       int64             `json:"at"` // unix millis
	Limits   []IntelStateLimit `json:"limits"`
	Pace     IntelStatePace    `json:"pace"`
	Trend    IntelStateTrend   `json:"trend"`
	Runway   IntelStateRunway  `json:"runway"`
	Roles    []IntelStateRole  `json:"roles"`
}

// NewIntelState builds the snapshot of in.
func NewIntelState(in Intel) IntelState {
	s := IntelState{
		Version:  intelStateVersion,
		Provider: in.Provider,
		At:       in.Now.UnixMilli(),
		Limits:   []IntelStateLimit{},
		Roles:    []IntelStateRole{},
		Pace: IntelStatePace{
			Label:         in.Pace.Label,
			TokensPerHour: in.Pace.TokensPerHour,
			PctPerHour:    in.Pace.PctPerHour,
		},
		Trend: IntelStateTrend{Label: in.Trend.Label, Ratio: in.Trend.Ratio},
		Runway: IntelStateRunway{
			Kind:         in.Runway.Kind,
			Lasts:        in.Runway.Lasts,
			UntilSeconds: int64(in.Runway.Until / time.Second),
			ResetSeconds: int64(in.Runway.Reset / time.Second),
		},
	}
	for _, l := range in.Limits {
		s.Limits = append(s.Limits, IntelStateLimit{
			Provider:   l.Provider,
			Window:     l.Window,
			Used:       l.Used,
			ResetsAt:   l.Reset.UnixMilli(),
			ObservedAt: l.ObservedAt.UnixMilli(),
		})
	}
	for _, r := range in.Roles {
		if len(s.Roles) == maxIntelStateRoles {
			break
		}
		s.Roles = append(s.Roles, IntelStateRole{Role: r.Role, Tokens: r.Tokens})
	}
	return s
}

// Signature is what makes two snapshots the same news: the limits at a whole
// percent and the reset minute, and the pace, trend and runway words. Token
// counts and the clock are left out, so a snapshot is only worth writing when
// something a reader would notice has moved.
func (s IntelState) Signature() string {
	var b strings.Builder
	for _, l := range s.Limits {
		fmt.Fprintf(&b, "%s|%s|%d|%d;", l.Provider, l.Window, int(l.Used*100+0.5), l.ResetsAt/60000)
	}
	fmt.Fprintf(&b, "%s;%s;%s;%t", s.Pace.Label, s.Trend.Label, s.Runway.Kind, s.Runway.Lasts)
	return b.String()
}

// ToEntry renders the snapshot as a session entry, copying the goal_state
// pattern.
func (s IntelState) ToEntry(parentID string) sess.Entry {
	data, err := json.Marshal(s)
	if err != nil {
		data = []byte("{}")
	}
	return sess.Entry{
		ID:       sess.MustID(),
		ParentID: parentID,
		Type:     EntryTypeIntelState,
		Content:  string(data),
	}
}
