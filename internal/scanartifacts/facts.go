package scanartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// maxFactNames bounds how many component names a BOMFacts bucket keeps. The
// names are for a one-line mention on a card, not an inventory listing.
const maxFactNames = 8

// BOMFacts is what a CycloneDX document says about itself: its generator's
// summary properties, its components broken down by ecosystem and category,
// and its vulnerabilities. It answers the questions an inventory scanner's
// result card asks (how many packages, algorithms, models), not only "how many
// findings".
type BOMFacts struct {
	// Metadata holds the document's vulnetix:* metadata properties, without
	// the vulnetix:env/* host facts.
	Metadata map[string]string
	// Components counts the top-level components.
	Components int
	// Ecosystems counts components by purl type (npm, golang, …).
	Ecosystems map[string]int
	// Categories counts components by their vulnetix:crypto/category or
	// vulnetix:ai/category property.
	Categories map[string]int
	// Names keeps up to maxFactNames component names per category.
	Names map[string][]string
	// PQC keeps up to maxFactNames component names per
	// vulnetix:crypto/pqc-status (quantum-safe, quantum-vulnerable,
	// deprecated, hybrid, …), with PQCCounts counting all of them.
	PQC       map[string][]string
	PQCCounts map[string]int
	// Vulns counts real vulnerabilities by severity; Licenses counts license
	// issues, which CycloneDX carries in the same array.
	Vulns    Counts
	Licenses int
}

// MetaInt returns a metadata property as a number, and false when it is
// absent or not a number.
func (f BOMFacts) MetaInt(name string) (int, bool) {
	v, ok := f.Metadata[name]
	if !ok {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// TopEcosystems returns the ecosystems, largest first (ties by name).
func (f BOMFacts) TopEcosystems() []Tally {
	return sortedTallies(f.Ecosystems)
}

// Tally is one named count.
type Tally struct {
	Name string
	N    int
}

func sortedTallies(m map[string]int) []Tally {
	out := make([]Tally, 0, len(m))
	for k, n := range m {
		out = append(out, Tally{Name: k, N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Name < out[j].Name
	})
	return out
}

type cdxComponent struct {
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	Purl       string    `json:"purl"`
	Properties []cdxProp `json:"properties"`
}

func componentProp(c *cdxComponent, name string) string {
	for _, p := range c.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// CycloneDXFacts reads a CycloneDX document's facts. It streams the
// components and vulnerabilities arrays, so a large SBOM is never held whole.
func CycloneDXFacts(ctx context.Context, path string, maxBytes int64) (BOMFacts, error) {
	f := BOMFacts{
		Metadata:   map[string]string{},
		Ecosystems: map[string]int{},
		Categories: map[string]int{},
		Names:      map[string][]string{},
		PQC:        map[string][]string{},
		PQCCounts:  map[string]int{},
	}
	file, err := os.Open(path)
	if err != nil {
		return f, err
	}
	defer file.Close()
	limit := int64(HardMaxBytes)
	if maxBytes > 0 && maxBytes < limit {
		limit = maxBytes
	}
	dec := json.NewDecoder(io.LimitReader(file, limit))
	dec.UseNumber()

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return f, fmt.Errorf("parse cyclonedx: not an object")
	}
	for dec.More() {
		if ctx.Err() != nil {
			return f, ctx.Err()
		}
		tok, err := dec.Token()
		if err != nil {
			return f, err
		}
		key, _ := tok.(string)
		switch key {
		case "metadata":
			var md struct {
				Properties []cdxProp `json:"properties"`
			}
			if err := dec.Decode(&md); err != nil {
				return f, err
			}
			for _, p := range md.Properties {
				if strings.HasPrefix(p.Name, "vulnetix:") && !strings.HasPrefix(p.Name, "vulnetix:env/") {
					f.Metadata[p.Name] = p.Value
				}
			}
		case "components":
			if err := streamArray(ctx, dec, func() error {
				var c cdxComponent
				if err := dec.Decode(&c); err != nil {
					return err
				}
				f.addComponent(&c)
				return nil
			}); err != nil {
				return f, err
			}
		case "vulnerabilities":
			if err := streamArray(ctx, dec, func() error {
				var vn cdxVuln
				if err := dec.Decode(&vn); err != nil {
					return err
				}
				if isLicense(&vn) {
					f.Licenses++
				} else {
					f.Vulns.Add(resolveCycloneDXSeverity(&vn), 1)
				}
				return nil
			}); err != nil {
				return f, err
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return f, err
			}
		}
	}
	return f, nil
}

func (f *BOMFacts) addComponent(c *cdxComponent) {
	f.Components++
	if t, _, ok := strings.Cut(strings.TrimPrefix(c.Purl, "pkg:"), "/"); ok && strings.HasPrefix(c.Purl, "pkg:") && t != "" {
		f.Ecosystems[t]++
	}
	cat := componentProp(c, "vulnetix:crypto/category")
	if cat == "" {
		cat = componentProp(c, "vulnetix:ai/category")
	}
	if cat != "" {
		f.Categories[cat]++
		if len(f.Names[cat]) < maxFactNames && c.Name != "" {
			f.Names[cat] = append(f.Names[cat], c.Name)
		}
	}
	if s := componentProp(c, "vulnetix:crypto/pqc-status"); s != "" {
		f.PQCCounts[s]++
		if len(f.PQC[s]) < maxFactNames && c.Name != "" {
			f.PQC[s] = append(f.PQC[s], c.Name)
		}
	}
}

// streamArray consumes one JSON array, calling each for every element. A
// null in place of the array is accepted as empty.
func streamArray(ctx context.Context, dec *json.Decoder, each func() error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if tok != json.Delim('[') {
		return fmt.Errorf("parse cyclonedx: expected an array")
	}
	for dec.More() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := each(); err != nil {
			return err
		}
	}
	_, err = dec.Token() // the closing ']'
	return err
}

// RunFacts is what a SARIF run says about the scan behind it: how many
// results and of what severity, how many rules fired out of how many were
// evaluated, and the scan properties some tools record (malscan's file and
// indicator counts and its verdict).
type RunFacts struct {
	Results        int
	Counts         Counts
	RulesTriggered int
	RulesEvaluated int
	FilesScanned   int
	Indicators     int
	Malicious      bool
}

// SARIFFacts reads the first run of a SARIF document. Suppressed and
// baseline-absent results are not counted, matching ParseSARIF.
func SARIFFacts(ctx context.Context, path string, maxBytes int64) (RunFacts, error) {
	var out RunFacts
	file, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer file.Close()
	data, err := budgetReadAll(file, maxBytes)
	if err != nil {
		return out, err
	}
	var doc struct {
		Runs []struct {
			Tool       sarifTool     `json:"tool"`
			Results    []sarifResult `json:"results"`
			Properties struct {
				FilesScanned   int  `json:"filesScanned"`
				IndicatorCount int  `json:"indicatorCount"`
				Malicious      bool `json:"malicious"`
			} `json:"properties"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return out, fmt.Errorf("parse sarif: %w", err)
	}
	if len(doc.Runs) == 0 {
		return out, nil
	}
	run := doc.Runs[0]
	rules := buildRuleMap(run.Tool)
	out.RulesEvaluated = len(run.Tool.Driver.Rules)
	for _, ext := range run.Tool.Extensions {
		out.RulesEvaluated += len(ext.Rules)
	}
	out.FilesScanned = run.Properties.FilesScanned
	out.Indicators = run.Properties.IndicatorCount
	out.Malicious = run.Properties.Malicious
	fired := map[string]bool{}
	for _, res := range run.Results {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if res.BaselineState == "absent" || len(res.Suppressions) > 0 {
			continue
		}
		sev, _ := severityForResult(res, rules)
		out.Counts.Add(sev, 1)
		out.Results++
		if res.RuleID != "" {
			fired[res.RuleID] = true
		}
	}
	out.RulesTriggered = len(fired)
	return out, nil
}
