package scanartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ratingScale discriminates among the incompatible score scales that can
// appear on one CycloneDX vulnerability's ratings array.
type ratingScale int

const (
	scaleUnknown     ratingScale = iota
	scaleCVSS                    // NVD, GitHub, OSV, RedHat, NPM, google_osi
	scaleProbability             // EPSS
	scaleDecision                // SSVC
	scaleVendor                  // Coalition ESS etc.
	scaleLicense                 // vulnetix-license-analyzer
)

// cvssSourceNames is the closed set of source names that carry a CVSS-scale
// score. Everything else is treated as vendor/probability/decision and left
// out of the headline vulnerability count.
var cvssSourceNames = map[string]bool{
	"NVD": true, "GitHub": true, "OSV": true, "RedHat": true,
	"NPM": true, "google_osi": true,
}

// scaleCVSSMethod maps method strings such as CVSSv31 to scaleCVSS.
var scaleCVSSMethod = map[string]bool{
	"CVSSv2": true, "CVSSv3": true, "CVSSv31": true, "CVSSv4": true,
}

func classifyRating(method, sourceName string) ratingScale {
	if scaleCVSSMethod[method] {
		return scaleCVSS
	}
	name := strings.TrimSpace(sourceName)
	switch {
	case cvssSourceNames[name]:
		return scaleCVSS
	case name == "EPSS":
		return scaleProbability
	case name == "SSVC":
		return scaleDecision
	case strings.Contains(name, "license"):
		return scaleLicense
	case strings.Contains(name, "Coalition"):
		return scaleVendor
	}
	return scaleVendor // fail closed: unrecognised scales are not headline vulns.
}

// cdxVuln is the minimal CycloneDX vulnerability shape.
type cdxAffect struct {
	Ref string `json:"ref"`
}

type cdxVuln struct {
	ID     string `json:"id"`
	Source struct {
		Name string `json:"name"`
	} `json:"source"`
	Ratings    []cdxRating `json:"ratings"`
	Properties []cdxProp   `json:"properties"`
	Affects    []cdxAffect `json:"affects"`
}

type cdxRating struct {
	Source struct {
		Name string `json:"name"`
	} `json:"source"`
	Score    json.Number `json:"score"`
	Severity string      `json:"severity"`
	Method   string      `json:"method"`
	Vector   string      `json:"vector"`
}

type cdxProp struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ParseCycloneDX reads a CycloneDX vulnerability document and returns counts,
// license counts, and per-vulnerability dedupe keys. It streams the
// vulnerabilities array so a 27 MB document does not explode memory.
func ParseCycloneDX(ctx context.Context, r io.Reader, maxBytes int64) (Counts, Counts, []FindingKey, error) {
	var counts, licenses Counts
	var keys []FindingKey

	dec := json.NewDecoder(r)
	dec.UseNumber()

	// Fast path: token-walk to the vulnerabilities array, then stream elements.
	var inVulnsArray bool
	for {
		if ctx.Err() != nil {
			return counts, licenses, keys, ctx.Err()
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return counts, licenses, keys, err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '[' && inVulnsArray {
				// Decode each element until the array closes.
				for dec.More() {
					if ctx.Err() != nil {
						return counts, licenses, keys, ctx.Err()
					}
					var vn cdxVuln
					if err := dec.Decode(&vn); err != nil {
						return counts, licenses, keys, err
					}
					if isLicense(&vn) {
						lic := SeverityFromWord(propValue(&vn, "vulnetix:license-severity"))
						licenses.Add(lic, 1)
						continue
					}
					sev := resolveCycloneDXSeverity(&vn)
					counts.Add(sev, 1)
					key := fmt.Sprintf("%s|%s", normalizeCdxID(vn.ID), strings.Join(affectsRefs(vn.Affects), ","))
					keys = append(keys, FindingKey{Key: key, Severity: sev})
				}
				// Consume closing ']'.
				if _, err := dec.Token(); err != nil {
					return counts, licenses, keys, err
				}
				inVulnsArray = false
			}
			continue
		}
		if s, ok := tok.(string); ok && s == "vulnerabilities" {
			inVulnsArray = true
		}
	}
	return counts, licenses, keys, nil
}

func affectsRefs(a []cdxAffect) []string {
	out := make([]string, 0, len(a))
	for _, x := range a {
		if x.Ref != "" {
			out = append(out, x.Ref)
		}
	}
	return out
}

func isLicense(vn *cdxVuln) bool {
	if strings.Contains(strings.ToLower(vn.Source.Name), "license") {
		return true
	}
	if propValue(vn, "vulnetix:license-severity") != "" {
		return true
	}
	for _, r := range vn.Ratings {
		if classifyRating(r.Method, r.Source.Name) == scaleLicense {
			return true
		}
	}
	return false
}

func propValue(vn *cdxVuln, name string) string {
	for _, p := range vn.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// resolveCycloneDXSeverity applies the precedence chain documented in the
// plan: vulnetix:max-severity > CVSS-scale score > CVSS-scale severity word
// > unknown with a warning.
func resolveCycloneDXSeverity(vn *cdxVuln) Severity {
	if m := propValue(vn, "vulnetix:max-severity"); m != "" {
		return SeverityFromWord(m)
	}
	var maxScore float64 = -1
	var scoreSev Severity
	for _, r := range vn.Ratings {
		if classifyRating(r.Method, r.Source.Name) != scaleCVSS {
			continue
		}
		if s, err := r.Score.Float64(); err == nil {
			rounded := float64(int(s*10+0.5)) / 10
			if rounded > maxScore {
				maxScore = rounded
				var v CVSSVersion
				if r.Vector != "" {
					v, _ = DetectVector(r.Vector)
				}
				scoreSev = BandFor(rounded, v)
				if scoreSev == SeverityUnknown && r.Severity != "" {
					scoreSev = SeverityFromWord(r.Severity)
				}
			}
		} else if r.Severity != "" {
			sev := SeverityFromWord(r.Severity)
			if sev > scoreSev {
				scoreSev = sev
			}
		}
	}
	if maxScore >= 0 {
		return scoreSev
	}
	// No CVSS score: fall back to the best severity word from CVSS-scale ratings.
	for _, r := range vn.Ratings {
		if classifyRating(r.Method, r.Source.Name) == scaleCVSS && r.Severity != "" {
			return SeverityFromWord(r.Severity)
		}
	}
	return SeverityUnknown
}

func normalizeCdxID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}
