package scanartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// sarifResult is a minimal result shape.
type sarifResult struct {
	RuleID        string                 `json:"ruleId"`
	RuleIndex     int                    `json:"ruleIndex"`
	Level         string                 `json:"level"`
	Properties    map[string]interface{} `json:"properties"`
	BaselineState string                 `json:"baselineState"`
	Suppressions  []struct{}             `json:"suppressions"`
	Locations     []struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
			Region struct {
				StartLine int `json:"startLine"`
			} `json:"region"`
		} `json:"physicalLocation"`
	} `json:"locations"`
	Fingerprints map[string]string `json:"fingerprints"`
}

type sarifRule struct {
	ID                   string                 `json:"id"`
	Properties           map[string]interface{} `json:"properties"`
	DefaultConfiguration *struct {
		Level      string                 `json:"level"`
		Properties map[string]interface{} `json:"properties"`
	} `json:"defaultConfiguration"`
}

type sarifTool struct {
	Driver struct {
		Rules []sarifRule `json:"rules"`
	} `json:"driver"`
	Extensions []struct {
		Rules []sarifRule `json:"rules"`
	} `json:"extensions"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifDoc struct {
	Runs []sarifRun `json:"runs"`
}

// ParseSARIF reads one run's results and returns counts, split into headline,
// suppressed, and inferred tallies, plus dedupe keys for headline results.
func ParseSARIF(ctx context.Context, r io.Reader, maxBytes int64) (Counts, Counts, int, []FindingKey, error) {
	var zero Counts

	data, err := budgetReadAll(r, maxBytes)
	if err != nil {
		return zero, zero, 0, nil, err
	}
	var doc sarifDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return zero, zero, 0, nil, fmt.Errorf("parse sarif: %w", err)
	}
	if len(doc.Runs) == 0 {
		return zero, zero, 0, nil, nil
	}
	run := doc.Runs[0]
	rules := buildRuleMap(run.Tool)

	var counts, suppressed Counts
	var inferred int
	var keys []FindingKey
	for _, res := range run.Results {
		if ctx.Err() != nil {
			return counts, suppressed, inferred, keys, ctx.Err()
		}
		if res.BaselineState == "absent" {
			continue
		}
		sev, fromRule := severityForResult(res, rules)
		if len(res.Suppressions) > 0 {
			suppressed.Add(sev, 1)
			continue
		}
		if !fromRule {
			inferred++
		}
		counts.Add(sev, 1)
		keys = append(keys, FindingKey{Key: dedupeKeyForResult(res), Severity: sev})
	}
	return counts, suppressed, inferred, keys, nil
}

func buildRuleMap(tool sarifTool) map[string]sarifRule {
	m := map[string]sarifRule{}
	for _, r := range tool.Driver.Rules {
		if r.ID != "" {
			m[r.ID] = r
		}
	}
	for _, ext := range tool.Extensions {
		for _, r := range ext.Rules {
			if r.ID != "" {
				m[r.ID] = r
			}
		}
	}
	return m
}

func severityForResult(res sarifResult, rules map[string]sarifRule) (Severity, bool) {
	// 1. security-severity property.
	if v, ok := res.Properties["security-severity"]; ok {
		if s := severityFromInterface(v, CVSSv31); s != SeverityUnknown {
			return s, true
		}
	}
	// 2. severity property word (Vulnetix convention).
	if v, ok := res.Properties["severity"]; ok {
		if word, ok := v.(string); ok {
			return SeverityFromWord(word), true
		}
	}
	// 3. result.level. This is a well-known default, but without a rule we
	// still flag it as inferred so the UI can disclose that.
	if sev := levelSeverity(res.Level); sev != SeverityUnknown {
		return sev, false
	}
	// 4. rule lookup.
	rule := rules[res.RuleID]
	if rule.ID == "" && res.RuleIndex >= 0 && res.RuleIndex < len(rules) {
		// Fallback by position if ruleId was empty.
	}
	if rule.ID != "" {
		if dc := rule.DefaultConfiguration; dc != nil {
			if sev := levelSeverity(dc.Level); sev != SeverityUnknown {
				return sev, true
			}
			if v, ok := dc.Properties["security-severity"]; ok {
				return severityFromInterface(v, CVSSv31), true
			}
			if v, ok := dc.Properties["severity"]; ok {
				return SeverityFromWord(stringFromInterface(v)), true
			}
		}
		if v, ok := rule.Properties["security-severity"]; ok {
			return severityFromInterface(v, CVSSv31), true
		}
		if v, ok := rule.Properties["severity"]; ok {
			return SeverityFromWord(stringFromInterface(v)), true
		}
	}
	// 5. SARIF default is warning, but mark it inferred.
	return SeverityMedium, false
}

func levelSeverity(level string) Severity {
	switch level {
	case "error":
		return SeverityHigh
	case "warning":
		return SeverityMedium
	case "note":
		return SeverityLow
	case "none":
		return SeverityNone
	}
	return SeverityUnknown
}

func severityFromInterface(v interface{}, version CVSSVersion) Severity {
	switch x := v.(type) {
	case string:
		if f, err := parseFloatString(x); err == nil {
			return BandFor(f, version)
		}
		return SeverityFromWord(x)
	case float64:
		return BandFor(x, version)
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return BandFor(f, version)
		}
	}
	return SeverityUnknown
}

func stringFromInterface(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		return ""
	}
}

func dedupeKeyForResult(res sarifResult) string {
	if fp := res.Fingerprints["vulnetix/v1"]; fp != "" {
		return fp
	}
	uri := ""
	line := 0
	if len(res.Locations) > 0 {
		uri = res.Locations[0].PhysicalLocation.ArtifactLocation.URI
		line = res.Locations[0].PhysicalLocation.Region.StartLine
	}
	return res.RuleID + "|" + uri + "|" + fmt.Sprintf("%d", line)
}
