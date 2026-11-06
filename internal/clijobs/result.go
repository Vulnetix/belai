package clijobs

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
)

// Result limits.
const (
	// MaxFindings caps the findings one result lists.
	MaxFindings = 500
	// MaxResultBytes caps a result document; a pulled one over it is dropped.
	MaxResultBytes = 192 << 10
	maxArtifacts   = 24
)

// Signals is the closed set of repository signals a profile may carry. The
// scout job emits only DeepSignals; the rest come from file names on the
// server.
var Signals = []string{
	"dockerfile", "compose", "helm", "k8s", "terraform", "cloudformation",
	"github-workflows", "openapi", "crypto", "ai-sdk", "agent-config", "sigstore",
	"release-artifacts", "vulnetix-config", "monorepo", "installed-deps", "license-file",
}

// DeepSignals are the signals only the scout job can establish.
var DeepSignals = []string{"crypto", "ai-sdk", "agent-config", "installed-deps", "release-artifacts"}

// Counts tallies findings by severity.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

// Finding is one finding reduced to identifiers.
type Finding struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Severity string `json:"severity"`
	Ref      string `json:"ref"`
}

// Result is the document a job leaves on its card (kanban Item.CliResult). It
// holds ids, counts and enums: never scanner output, advisory text or a
// matched value.
type Result struct {
	Job       string    `json:"job"`
	ExitCode  int       `json:"exitCode"`
	Counts    Counts    `json:"counts"`
	Findings  []Finding `json:"findings"`
	Signals   []string  `json:"signals"`
	Artifacts []string  `json:"artifacts"`
}

var (
	idRE       = regexp.MustCompile(`^[A-Za-z0-9._:@+~-]{1,120}$`)
	kindRE     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	pathRE     = regexp.MustCompile(`^[A-Za-z0-9._@+/~:-]{0,200}$`)
	refRE      = regexp.MustCompile(`^[A-Za-z0-9._:@+/~-]{0,80}$`)
	artifactRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	severities = []string{"critical", "high", "medium", "low", "info", "unknown"}
)

// Add records a finding, keeping the first MaxFindings, and counts its severity
// in Counts for the four counted levels. It returns false once the list is
// full (the count still moves, so a truncated list does not hide volume).
func (r *Result) Add(f Finding) bool {
	switch f.Severity {
	case "critical":
		r.Counts.Critical++
	case "high":
		r.Counts.High++
	case "medium":
		r.Counts.Medium++
	case "low":
		r.Counts.Low++
	}
	if len(r.Findings) >= MaxFindings {
		return false
	}
	r.Findings = append(r.Findings, f)
	return true
}

func (f Finding) valid() bool {
	return idRE.MatchString(f.ID) && kindRE.MatchString(f.Kind) && pathRE.MatchString(f.Path) &&
		!hasDotDot(f.Path) && slices.Contains(severities, f.Severity) && refRE.MatchString(f.Ref)
}

func hasDotDot(p string) bool { return bytes.Contains([]byte(p), []byte("..")) }

// Normalize sorts the lists and drops anything outside the closed shapes. It is
// how the harness finishes a result it built, so Clean accepts it unchanged.
func (r *Result) Normalize() {
	kept := r.Findings[:0]
	for _, f := range r.Findings {
		if f.valid() {
			kept = append(kept, f)
		}
	}
	r.Findings = kept
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	if len(r.Findings) > MaxFindings {
		r.Findings = r.Findings[:MaxFindings]
	}
	r.Signals = cleanSignals(r.Signals)
	var arts []string
	for _, a := range r.Artifacts {
		if artifactRE.MatchString(a) && !slices.Contains(arts, a) && len(arts) < maxArtifacts {
			arts = append(arts, a)
		}
	}
	sort.Strings(arts)
	if arts == nil {
		arts = []string{}
	}
	r.Artifacts = arts
}

func cleanSignals(in []string) []string {
	out := []string{}
	for _, s := range in {
		if slices.Contains(Signals, s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// JSON returns the canonical document for the result: normalised, compact, in
// the field order above.
func (r Result) JSON() (string, error) {
	r.Normalize()
	b, err := json.Marshal(r)
	return string(b), err
}

// Clean validates a result document and returns its canonical form. It accepts
// only the closed shape: an unknown key, an unknown job or signal, a finding
// field outside its pattern, more than MaxFindings findings or a document over
// MaxResultBytes is refused whole. Nothing is repaired, because the document
// came from another host.
func Clean(doc string) (string, bool) {
	if doc == "" || len(doc) > MaxResultBytes {
		return "", false
	}
	var r Result
	dec := json.NewDecoder(bytes.NewReader([]byte(doc)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil || dec.More() {
		return "", false
	}
	switch {
	case !Valid(r.Job), r.ExitCode < -1 || r.ExitCode > 255, len(r.Findings) > MaxFindings, len(r.Artifacts) > maxArtifacts:
		return "", false
	case r.Counts.Critical < 0 || r.Counts.High < 0 || r.Counts.Medium < 0 || r.Counts.Low < 0:
		return "", false
	}
	for _, f := range r.Findings {
		if !f.valid() {
			return "", false
		}
	}
	for _, s := range r.Signals {
		if !slices.Contains(Signals, s) {
			return "", false
		}
	}
	for _, a := range r.Artifacts {
		if !artifactRE.MatchString(a) {
			return "", false
		}
	}
	out, err := r.JSON()
	if err != nil || len(out) > MaxResultBytes {
		return "", false
	}
	return out, true
}
