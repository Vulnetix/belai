package scanartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Records returns one line per record of the artifact at path (a SARIF result,
// a CycloneDX component or vulnerability, an OpenVEX statement), for the
// retrieval index. Unlike the triage renderers it is not capped at a few
// hundred lines: the index applies its own token budget.
//
// A line is composed from parsed identifier fields only: rule or advisory id,
// severity, package name, version and purl, file and line, status and
// justification. It never carries a result's message, a code region or a
// snippet, so a secret scanner's matched value cannot reach the index through
// it. Other kinds return nil, nil.
func Records(ctx context.Context, kind Kind, path string, maxBytes int64) ([]string, error) {
	switch kind {
	case KindSARIF:
		return sarifRecords(ctx, path, maxBytes)
	case KindCycloneDXSBOM, KindCycloneDXCBOM, KindCycloneDXAIBOM:
		return cycloneDXRecords(ctx, path, maxBytes)
	case KindOpenVEX, KindOpenVEXRiskAccepted:
		return openVEXRecords(ctx, path, maxBytes)
	}
	return nil, nil
}

func sarifRecords(ctx context.Context, path string, maxBytes int64) ([]string, error) {
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
	var out []string
	for _, run := range doc.Runs {
		rules := buildRuleMap(run.Tool)
		for _, res := range run.Results {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			if res.BaselineState == "absent" {
				continue
			}
			sev, _ := severityForResult(res, rules)
			uri, line := firstSARIFLocation(res)
			rec := fmt.Sprintf("SARIF result rule %s severity %s", res.RuleID, sev.String())
			if uri != "" {
				rec += fmt.Sprintf(" in %s line %d", uri, line)
			}
			if len(res.Suppressions) > 0 {
				rec += " suppressed"
			}
			out = append(out, rec)
		}
	}
	return out, nil
}

func cycloneDXRecords(ctx context.Context, path string, maxBytes int64) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	limit := int64(HardMaxBytes)
	if maxBytes > 0 && maxBytes < limit {
		limit = maxBytes
	}
	dec := json.NewDecoder(io.LimitReader(file, limit))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("parse cyclonedx: not an object")
	}
	var out []string
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		tok, err := dec.Token()
		if err != nil {
			return out, err
		}
		switch key, _ := tok.(string); key {
		case "components":
			err = streamArray(ctx, dec, func() error {
				var c cdxComponent
				if err := dec.Decode(&c); err != nil {
					return err
				}
				if c.Name == "" {
					return nil
				}
				rec := "CycloneDX component " + c.Name
				if c.Version != "" {
					rec += " version " + c.Version
				}
				if c.Type != "" {
					rec += " type " + c.Type
				}
				if c.Purl != "" {
					rec += " purl " + c.Purl
				}
				out = append(out, rec)
				return nil
			})
		case "vulnerabilities":
			err = streamArray(ctx, dec, func() error {
				var vn cdxVuln
				if err := dec.Decode(&vn); err != nil {
					return err
				}
				if isLicense(&vn) {
					return nil
				}
				rec := fmt.Sprintf("CycloneDX vulnerability %s severity %s", normalizeCdxID(vn.ID), resolveCycloneDXSeverity(&vn).String())
				if refs := affectsRefs(vn.Affects); len(refs) > 0 {
					rec += " affects " + strings.Join(refs, " ")
				}
				out = append(out, rec)
				return nil
			})
		default:
			var skip json.RawMessage
			err = dec.Decode(&skip)
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func openVEXRecords(ctx context.Context, path string, maxBytes int64) ([]string, error) {
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
	var out []string
	for _, st := range doc.Statements {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		rec := fmt.Sprintf("OpenVEX statement vulnerability %s status %s", st.Vulnerability.Name, strings.ToLower(st.Status))
		if j := strings.TrimSpace(st.Justification); j != "" {
			rec += " justification " + j
		}
		for _, p := range st.Products {
			if p.ID != "" {
				rec += " product " + p.ID
			}
		}
		out = append(out, rec)
	}
	return out, nil
}
