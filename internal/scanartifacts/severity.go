package scanartifacts

import (
	"fmt"
	"strings"
)

// Severity is a normalised severity bucket.
type Severity int

const (
	SeverityUnknown Severity = iota
	SeverityNone
	SeverityInfo
	SeverityLow
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityNone:
		return "none"
	case SeverityInfo:
		return "info"
	case SeverityLow:
		return "low"
	case SeverityMedium:
		return "medium"
	case SeverityHigh:
		return "high"
	case SeverityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// SeverityFromWord maps a severity word to a Severity, case-insensitively.
func SeverityFromWord(s string) Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "negligible":
		return SeverityNone
	case "info", "informational", "information":
		return SeverityInfo
	case "low":
		return SeverityLow
	case "medium", "moderate":
		return SeverityMedium
	case "high":
		return SeverityHigh
	case "critical", "severe":
		return SeverityCritical
	default:
		return SeverityUnknown
	}
}

// Counts holds per-severity tallies.
type Counts struct {
	Critical, High, Medium, Low, Info, None, Unknown int
}

// Add increments the bucket for s by n.
func (c *Counts) Add(s Severity, n int) {
	switch s {
	case SeverityCritical:
		c.Critical += n
	case SeverityHigh:
		c.High += n
	case SeverityMedium:
		c.Medium += n
	case SeverityLow:
		c.Low += n
	case SeverityInfo:
		c.Info += n
	case SeverityNone:
		c.None += n
	default:
		c.Unknown += n
	}
}

// Total returns the sum of all vulnerability buckets.
func (c Counts) Total() int {
	return c.Critical + c.High + c.Medium + c.Low + c.Info + c.None + c.Unknown
}

// Max returns the highest severity present.
func (c Counts) Max() Severity {
	if c.Critical > 0 {
		return SeverityCritical
	}
	if c.High > 0 {
		return SeverityHigh
	}
	if c.Medium > 0 {
		return SeverityMedium
	}
	if c.Low > 0 {
		return SeverityLow
	}
	if c.Info > 0 {
		return SeverityInfo
	}
	if c.None > 0 {
		return SeverityNone
	}
	return SeverityUnknown
}

// Merge adds o into c and returns the result without mutating inputs.
func (c Counts) Merge(o Counts) Counts {
	c.Critical += o.Critical
	c.High += o.High
	c.Medium += o.Medium
	c.Low += o.Low
	c.Info += o.Info
	c.None += o.None
	c.Unknown += o.Unknown
	return c
}

// Summary is a roll-up across all artifacts in a project.
type Summary struct {
	Dir           string                 `json:"dir"`
	Artifacts     []Artifact             `json:"artifacts"`
	PerFile       map[string]FileSummary `json:"per_file"`
	Union         Counts                 `json:"union"`
	Licenses      Counts                 `json:"licenses"`
	Suppressed    Counts                 `json:"suppressed"`
	RiskAccepted  Counts                 `json:"risk_accepted"`
	Inferred      int                    `json:"inferred"`
	Fingerprint   string                 `json:"fingerprint"`
	SchemaVersion int                    `json:"schema_version"`
}

// FileSummary holds counts for one artifact.
type FileSummary struct {
	Counts     Counts `json:"counts"`
	Inferred   int    `json:"inferred"`
	Suppressed int    `json:"suppressed"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// HasFindings reports whether any vulnerability bucket is non-zero.
func (s Summary) HasFindings() bool {
	return s.Union.Total() > 0
}

// FindingKey is one deduplicated vulnerability plus its normalised severity.
type FindingKey struct {
	Key      string
	Severity Severity
}

// UniqueKeys returns the unique keys from a slice, preserving first-seen
// severity.
func UniqueKeys(items []FindingKey) []FindingKey {
	seen := map[string]bool{}
	out := items[:0]
	for _, it := range items {
		if seen[it.Key] {
			continue
		}
		seen[it.Key] = true
		out = append(out, it)
	}
	return out
}

// Format returns a compact human-readable summary.
func (s Summary) Format() string {
	u := s.Union
	return fmt.Sprintf("%d critical · %d high · %d medium · %d low · %d unknown", u.Critical, u.High, u.Medium, u.Low, u.Unknown)
}
