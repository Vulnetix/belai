package scanartifacts

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// CVSSVersion is the detected CVSS standard.
type CVSSVersion int

const (
	CVSSUnknown CVSSVersion = iota
	CVSSv2
	CVSSv30
	CVSSv31
	CVSSv40
)

// CVSS prefixes.
const (
	prefixV40 = "CVSS:4.0/"
	prefixV31 = "CVSS:3.1/"
	prefixV30 = "CVSS:3.0/"
	prefixV20 = "CVSS:2.0/"
)

var vectorRe = regexp.MustCompile(`^CVSS:(\d\.\d)/|` + "^" + regexp.QuoteMeta("CWSS:") + `|(?i)` + "^" + regexp.QuoteMeta("CWSS:") + ``)

// DetectVector classifies a vector string. It rejects CWSS explicitly.
func DetectVector(s string) (CVSSVersion, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "CWSS:") {
		return CVSSUnknown, false
	}
	switch {
	case strings.HasPrefix(s, prefixV40):
		return CVSSv40, true
	case strings.HasPrefix(s, prefixV31):
		return CVSSv31, true
	case strings.HasPrefix(s, prefixV30):
		return CVSSv30, true
	case strings.HasPrefix(s, prefixV20):
		return CVSSv2, true
	case strings.Contains(s, "Au:") && strings.Contains(s, "AV:"):
		return CVSSv2, true
	}
	return CVSSUnknown, false
}

// BaseScore converts a CVSS vector into a base score. v2 and v3.0/v3.1 are
// fully implemented; v4.0 is not, so it returns ok=false even though the
// version is detected.
func BaseScore(vec string) (float64, CVSSVersion, bool) {
	v, ok := DetectVector(vec)
	if !ok {
		return 0, CVSSUnknown, false
	}
	switch v {
	case CVSSv2:
		score, err := cvss2Score(vec)
		return score, v, err == nil
	case CVSSv30, CVSSv31:
		score, err := cvss3Score(vec)
		return score, v, err == nil
	case CVSSv40:
		return 0, v, false
	}
	return 0, CVSSUnknown, false
}

// BandFor maps a CVSS score to a severity bucket. Scores are rounded to one
// decimal place before comparing because JSON float32 round-trips produce
// boundary noise such as 9.300000190734863.
func BandFor(score float64, v CVSSVersion) Severity {
	r := math.Round(score*10) / 10
	switch {
	case r == 0:
		return SeverityNone
	case r >= 0.1 && r <= 3.9:
		return SeverityLow
	case r >= 4.0 && r <= 6.9:
		return SeverityMedium
	case r >= 7.0 && r <= 8.9:
		return SeverityHigh
	case r >= 9.0 && r <= 10.0:
		if v == CVSSv2 {
			return SeverityHigh // CVSS v2 has no critical band.
		}
		return SeverityCritical
	default:
		return SeverityUnknown
	}
}

// metric extracts a metric value from a vector string.
func metric(vec, key string) string {
	for _, part := range strings.Split(vec, "/") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, key+":") {
			return strings.TrimPrefix(part, key+":")
		}
	}
	return ""
}

// mapAV converts access vector metrics.
var avMap = map[string]float64{
	"N": 1.0, "A": 0.646, "L": 0.395, "P": 0.0,
}

// mapAC converts access complexity metrics.
var acMap = map[string]float64{
	"L": 0.77, "M": 0.64, "H": 0.44,
}

// mapAu converts authentication metrics.
var auMap = map[string]float64{
	"M": 0.45, "S": 0.56, "N": 0.704,
}

// cvss2Score implements the CVSS v2 base equation.
func cvss2Score(vec string) (float64, error) {
	av, ok := avMap[strings.ToUpper(metric(vec, "AV"))]
	if !ok {
		return 0, fmt.Errorf("unknown AV")
	}
	ac, ok := acMap[strings.ToUpper(metric(vec, "AC"))]
	if !ok {
		return 0, fmt.Errorf("unknown AC")
	}
	au, ok := auMap[strings.ToUpper(metric(vec, "Au"))]
	if !ok {
		return 0, fmt.Errorf("unknown Au")
	}
	c := impactMap(strings.ToUpper(metric(vec, "C")))
	i := impactMap(strings.ToUpper(metric(vec, "I")))
	a := impactMap(strings.ToUpper(metric(vec, "A")))
	impact := 10.41 * (1 - (1-c)*(1-i)*(1-a))
	exploit := 20 * au * ac * av
	f := 0.0
	if impact == 0 {
		f = 0
	} else {
		f = 1.176
	}
	score := ((0.6 * impact) + (0.4 * exploit) - 1.5) * f
	if score < 0 {
		score = 0
	}
	return math.Min(10, score), nil
}

func impactMap(s string) float64 {
	switch s {
	case "C":
		return 0.660
	case "P":
		return 0.275
	case "N":
		return 0
	}
	return 0
}

// map3AV converts CVSS v3 access vector metrics.
var map3AV = map[string]float64{
	"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2,
}

// map3AC converts CVSS v3 attack complexity.
var map3AC = map[string]float64{
	"L": 0.77, "H": 0.44,
}

// map3PR defines privileges-required with unchanged scope.
var map3PR = map[string]float64{
	"N": 0.85, "L": 0.62, "H": 0.27,
}

// map3PRChanged defines privileges-required with changed scope.
var map3PRChanged = map[string]float64{
	"N": 0.85, "L": 0.68, "H": 0.5,
}

// map3UI converts user interaction.
var map3UI = map[string]float64{
	"N": 0.85, "R": 0.62,
}

// map3C, map3I, map3A map C/I/A to impact coefficients.
var map3CIA = map[string]float64{
	"H": 0.56, "L": 0.22, "N": 0,
}

// cvss3Score implements the CVSS v3.0/v3.1 base equation.
func cvss3Score(vec string) (float64, error) {
	av, ok := map3AV[strings.ToUpper(metric(vec, "AV"))]
	if !ok {
		return 0, fmt.Errorf("unknown AV")
	}
	ac, ok := map3AC[strings.ToUpper(metric(vec, "AC"))]
	if !ok {
		return 0, fmt.Errorf("unknown AC")
	}
	prRaw := strings.ToUpper(metric(vec, "PR"))
	ui, ok := map3UI[strings.ToUpper(metric(vec, "UI"))]
	if !ok {
		return 0, fmt.Errorf("unknown UI")
	}
	c := map3CIA[strings.ToUpper(metric(vec, "C"))]
	i := map3CIA[strings.ToUpper(metric(vec, "I"))]
	a := map3CIA[strings.ToUpper(metric(vec, "A"))]

	scopeChanged := strings.ToUpper(metric(vec, "S")) == "C"
	var pr float64
	if scopeChanged {
		pr = map3PRChanged[prRaw]
	} else {
		pr = map3PR[prRaw]
	}
	if pr == 0 && prRaw != "" {
		return 0, fmt.Errorf("unknown PR")
	}

	iss := 1 - ((1 - c) * (1 - i) * (1 - a))
	var impact float64
	if scopeChanged {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	} else {
		impact = 6.42 * iss
	}
	exploit := 8.22 * av * ac * pr * ui
	score := 0.0
	if scopeChanged {
		if impact <= 0 {
			score = 0
		} else {
			score = math.Min(10, 1.08*(impact+exploit))
		}
	} else {
		score = math.Min(10, impact+exploit)
	}
	if score < 0 {
		score = 0
	}
	return score, nil
}
