package scanartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Finding rendering bounds. A triage turn gets one bounded text block per
// report, not the whole artifact, so a 27 MB CycloneDX document cannot flood
// the model's context.
const (
	maxFindingLines     = 200
	maxFindingLineChars = 200
)

// FindingLine is one bounded, human-readable finding line for the triage turn.
type FindingLine struct {
	Text string
}

// renderFindingLines clamps lines to the triage bounds and clips over-long
// lines with an ellipsis.
func renderFindingLines(lines []string) []string {
	out := make([]string, 0, min(len(lines), maxFindingLines))
	for _, ln := range lines {
		if len(out) >= maxFindingLines {
			break
		}
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if len(ln) > maxFindingLineChars {
			ln = ln[:maxFindingLineChars] + "…"
		}
		out = append(out, ln)
	}
	return out
}

// SARIFFindingLines renders SARIF results as "rule severity file:line" lines,
// reusing the same result/rule shapes the summary parser uses. Suppressed and
// absent-baseline results are skipped.
func SARIFFindingLines(ctx context.Context, path string, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := budgetReadAll(f, maxBytes)
	if err != nil {
		return nil, err
	}
	var doc sarifDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse sarif: %w", err)
	}
	if len(doc.Runs) == 0 {
		return nil, nil
	}
	run := doc.Runs[0]
	rules := buildRuleMap(run.Tool)

	var lines []string
	for _, res := range run.Results {
		if ctx.Err() != nil {
			return lines, ctx.Err()
		}
		if res.BaselineState == "absent" || len(res.Suppressions) > 0 {
			continue
		}
		sev, _ := severityForResult(res, rules)
		uri, line := firstSARIFLocation(res)
		if uri == "" {
			uri = res.RuleID
		}
		lines = append(lines, fmt.Sprintf("%s %s %s:%d", res.RuleID, sev.String(), uri, line))
	}
	return renderFindingLines(lines), nil
}

func firstSARIFLocation(res sarifResult) (string, int) {
	if len(res.Locations) == 0 {
		return "", 0
	}
	loc := res.Locations[0]
	return loc.PhysicalLocation.ArtifactLocation.URI, loc.PhysicalLocation.Region.StartLine
}

// CycloneDXFindingLines renders CycloneDX vulnerabilities as
// "id severity affected-refs" lines, streaming the vulnerabilities array with
// the same token-walk ParseCycloneDX uses. License entries are skipped.
func CycloneDXFindingLines(ctx context.Context, path string, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(io.LimitReader(f, maxBytes))
	dec.UseNumber()

	var lines []string
	var inVulnsArray bool
	for {
		if ctx.Err() != nil {
			return lines, ctx.Err()
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return lines, err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '[' && inVulnsArray {
				for dec.More() {
					if ctx.Err() != nil {
						return lines, ctx.Err()
					}
					var vn cdxVuln
					if err := dec.Decode(&vn); err != nil {
						return lines, err
					}
					if isLicense(&vn) {
						continue
					}
					sev := resolveCycloneDXSeverity(&vn)
					lines = append(lines, fmt.Sprintf("%s %s %s", normalizeCdxID(vn.ID), sev.String(), strings.Join(affectsRefs(vn.Affects), ",")))
					if len(lines) >= maxFindingLines {
						// Stop early: further findings would exceed the triage bound.
						// Consume the rest is unnecessary because we close the file.
						return renderFindingLines(lines), nil
					}
				}
				if _, err := dec.Token(); err != nil {
					return lines, err
				}
				inVulnsArray = false
			}
			continue
		}
		if s, ok := tok.(string); ok && s == "vulnerabilities" {
			inVulnsArray = true
		}
	}
	return renderFindingLines(lines), nil
}

// OpenVEXFindingLines renders OpenVEX statements as "name status impact"
// lines, reusing the VEX document shape the summary parser uses.
func OpenVEXFindingLines(ctx context.Context, path string, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := budgetReadAll(f, maxBytes)
	if err != nil {
		return nil, err
	}
	var doc vexDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse vex: %w", err)
	}
	var lines []string
	for _, st := range doc.Statements {
		if ctx.Err() != nil {
			return lines, ctx.Err()
		}
		impact := strings.TrimSpace(st.ImpactStatement)
		if impact == "" {
			impact = "-"
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", st.Vulnerability.Name, strings.ToLower(st.Status), impact))
	}
	return renderFindingLines(lines), nil
}
