package scanartifacts

import (
	"encoding/json"
	"testing"
)

func TestLevelSeverity(t *testing.T) {
	cases := []struct {
		in   string
		want Severity
	}{
		{"error", SeverityHigh},
		{"warning", SeverityMedium},
		{"note", SeverityLow},
		{"none", SeverityNone},
		{"", SeverityUnknown},
		{"bogus", SeverityUnknown},
	}
	for _, c := range cases {
		if got := levelSeverity(c.in); got != c.want {
			t.Errorf("levelSeverity(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSeverityFromInterface(t *testing.T) {
	cases := []struct {
		in   interface{}
		want Severity
	}{
		{"9.8", SeverityCritical},
		{"7.5", SeverityHigh},
		{"critical", SeverityCritical},
		{"high", SeverityHigh},
		{9.8, SeverityCritical},
		{7.5, SeverityHigh},
		{4.0, SeverityMedium},
		{json.Number("9.8"), SeverityCritical},
		{json.Number("not-a-number"), SeverityUnknown},
		{true, SeverityUnknown},
		{nil, SeverityUnknown},
		{42, SeverityUnknown},
	}
	for _, c := range cases {
		if got := severityFromInterface(c.in, CVSSv31); got != c.want {
			t.Errorf("severityFromInterface(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestStringFromInterface(t *testing.T) {
	cases := []struct {
		in   interface{}
		want string
	}{
		{"high", "high"},
		{123, ""},
		{nil, ""},
		{true, ""},
	}
	for _, c := range cases {
		if got := stringFromInterface(c.in); got != c.want {
			t.Errorf("stringFromInterface(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDedupeKeyForResult(t *testing.T) {
	withFP := sarifResult{
		RuleID:       "VNX-001",
		Fingerprints: map[string]string{"vulnetix/v1": "fp-abc"},
	}
	if got := dedupeKeyForResult(withFP); got != "fp-abc" {
		t.Fatalf("dedupeKeyForResult(fingerprint) = %q, want fp-abc", got)
	}

	withLoc := sarifResult{
		RuleID: "VNX-002",
		Locations: []struct {
			PhysicalLocation struct {
				ArtifactLocation struct {
					URI string `json:"uri"`
				} `json:"artifactLocation"`
				Region struct {
					StartLine int `json:"startLine"`
				} `json:"region"`
			} `json:"physicalLocation"`
		}{
			{
				PhysicalLocation: struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				}{
					ArtifactLocation: struct {
						URI string `json:"uri"`
					}{URI: "a.go"},
					Region: struct {
						StartLine int `json:"startLine"`
					}{StartLine: 42},
				},
			},
		},
	}
	if got := dedupeKeyForResult(withLoc); got != "VNX-002|a.go|42" {
		t.Fatalf("dedupeKeyForResult(location) = %q", got)
	}

	if got := dedupeKeyForResult(sarifResult{RuleID: "VNX-003"}); got != "VNX-003||0" {
		t.Fatalf("dedupeKeyForResult(no loc) = %q", got)
	}
}

func TestBuildRuleMap(t *testing.T) {
	tool := sarifTool{}
	tool.Driver.Rules = []sarifRule{{ID: "A"}, {ID: ""}, {ID: "B"}}
	tool.Extensions = []struct {
		Rules []sarifRule `json:"rules"`
	}{
		{Rules: []sarifRule{{ID: "C"}, {ID: "B"}}}, // extension duplicate of B
	}
	m := buildRuleMap(tool)
	if len(m) != 3 {
		t.Fatalf("buildRuleMap len = %d, want 3 (%+v)", len(m), m)
	}
	for _, id := range []string{"A", "B", "C"} {
		if _, ok := m[id]; !ok {
			t.Errorf("missing rule %q in map %+v", id, m)
		}
	}
}

func TestSeverityForResultRuleConfig(t *testing.T) {
	// Rule with defaultConfiguration.level resolved via rule lookup.
	levelCfg := func(l string) *struct {
		Level      string                 `json:"level"`
		Properties map[string]interface{} `json:"properties"`
	} {
		return &struct {
			Level      string                 `json:"level"`
			Properties map[string]interface{} `json:"properties"`
		}{Level: l}
	}

	rules := map[string]sarifRule{
		"R-LEVEL": {ID: "R-LEVEL", DefaultConfiguration: levelCfg("error")},
		"R-SECSEC": {
			ID:         "R-SECSEC",
			Properties: map[string]interface{}{"security-severity": "9.8"},
		},
		"R-SEV": {
			ID:         "R-SEV",
			Properties: map[string]interface{}{"severity": "critical"},
		},
		"R-DC-SEV": {
			ID:                   "R-DC-SEV",
			DefaultConfiguration: levelCfg(""),
			Properties:           map[string]interface{}{"severity": "low"},
		},
	}

	cases := []struct {
		res      sarifResult
		wantSev  Severity
		wantRule bool
	}{
		{sarifResult{RuleID: "R-LEVEL"}, SeverityHigh, true},
		{sarifResult{RuleID: "R-SECSEC"}, SeverityCritical, true},
		{sarifResult{RuleID: "R-SEV"}, SeverityCritical, true},
		{sarifResult{RuleID: "R-DC-SEV"}, SeverityLow, true},
		// result-level before rule lookup, marked inferred.
		{sarifResult{RuleID: "UNKNOWN", Level: "error"}, SeverityHigh, false},
		// final fallback: SARIF default warning, inferred.
		{sarifResult{RuleID: "UNKNOWN"}, SeverityMedium, false},
	}
	for _, c := range cases {
		sev, fromRule := severityForResult(c.res, rules)
		if sev != c.wantSev || fromRule != c.wantRule {
			t.Errorf("severityForResult(rule=%q) = (%v, %v), want (%v, %v)",
				c.res.RuleID, sev, fromRule, c.wantSev, c.wantRule)
		}
	}
}
