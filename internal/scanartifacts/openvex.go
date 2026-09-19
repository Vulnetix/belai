package scanartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// vexStatement is a minimal OpenVEX statement shape.
type vexStatement struct {
	Vulnerability struct {
		Name string `json:"name"`
	} `json:"vulnerability"`
	Products []struct {
		ID string `json:"@id"`
	} `json:"products"`
	Status          string `json:"status"`
	ImpactStatement string `json:"impact_statement"`
	Action          struct {
		Status string `json:"status"`
	} `json:"action,omitempty"`
}

// vexDoc is the minimal OpenVEX document shape.
type vexDoc struct {
	Statements []vexStatement `json:"statements"`
}

// ParseOpenVEX reads a VEX document and returns headline counts, the number
// under investigation, the number risk-accepted, and dedupe keys.
func ParseOpenVEX(ctx context.Context, r io.Reader, maxBytes int64) (Counts, int, int, []FindingKey, error) {
	var zero Counts
	data, err := budgetReadAll(r, maxBytes)
	if err != nil {
		return zero, 0, 0, nil, err
	}
	var doc vexDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return zero, 0, 0, nil, fmt.Errorf("parse vex: %w", err)
	}

	seen := map[string]bool{}
	var counts Counts
	var investigating, riskAccepted int
	var keys []FindingKey
	for _, st := range doc.Statements {
		if ctx.Err() != nil {
			return counts, investigating, riskAccepted, keys, ctx.Err()
		}
		status := strings.ToLower(st.Status)
		key := vexDedupeKey(st)
		if seen[key] {
			continue
		}
		seen[key] = true

		if strings.Contains(strings.ToLower(st.Action.Status), "risk-accepted") {
			riskAccepted++
			continue
		}
		sev := SeverityUnknown
		switch status {
		case "affected":
			if imp := st.ImpactStatement; imp != "" {
				sev = severityFromImpactStatement(imp)
			}
			counts.Add(sev, 1)
		case "under_investigation":
			investigating++
		case "fixed", "not_affected":
			// Exclude from counts.
		}
		keys = append(keys, FindingKey{Key: key, Severity: sev})
	}
	return counts, investigating, riskAccepted, keys, nil
}

func severityFromImpactStatement(text string) Severity {
	low := strings.ToLower(text)
	for _, word := range []string{"critical", "high", "medium", "low", "info", "none"} {
		if strings.Contains(low, "severity: "+word) {
			return SeverityFromWord(word)
		}
	}
	return SeverityUnknown
}

func vexDedupeKey(st vexStatement) string {
	ids := make([]string, 0, len(st.Products))
	for _, p := range st.Products {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return st.Vulnerability.Name + "|" + strings.ToLower(st.Status) + "|" + strings.Join(ids, ",") + "|" + strings.ToLower(st.Action.Status)
}
