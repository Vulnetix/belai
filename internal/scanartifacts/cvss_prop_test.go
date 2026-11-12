package scanartifacts

import (
	"math"
	"sort"
	"strings"
	"testing"
)

// scoredVector is one vector of the full metric space with the score and
// band the package gives it.
type scoredVector struct {
	vec   string
	v     CVSSVersion
	score float64
	band  Severity
}

// sortedKeys returns a metric map's values in a fixed order, so a failure
// names the same vector on every run.
func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// impactValues are the C/I/A values impactMap knows; it is a switch, not a
// map, so they are listed here.
var impactValues = []string{"C", "P", "N"}

// allCVSS2Vectors enumerates every v2 base vector over avMap, acMap, auMap
// and impactMap.
func allCVSS2Vectors() []string {
	var out []string
	for _, av := range sortedKeys(avMap) {
		for _, ac := range sortedKeys(acMap) {
			for _, au := range sortedKeys(auMap) {
				for _, c := range impactValues {
					for _, i := range impactValues {
						for _, a := range impactValues {
							out = append(out, prefixV20+"AV:"+av+"/AC:"+ac+"/Au:"+au+"/C:"+c+"/I:"+i+"/A:"+a)
						}
					}
				}
			}
		}
	}
	return out
}

// allCVSS3Vectors enumerates every v3.0 and v3.1 base vector over map3AV,
// map3AC, map3PR (map3PRChanged has the same keys), map3UI, both scopes and
// map3CIA.
func allCVSS3Vectors() []string {
	var out []string
	for _, prefix := range []string{prefixV30, prefixV31} {
		for _, av := range sortedKeys(map3AV) {
			for _, ac := range sortedKeys(map3AC) {
				for _, pr := range sortedKeys(map3PR) {
					for _, ui := range sortedKeys(map3UI) {
						for _, s := range []string{"U", "C"} {
							for _, c := range sortedKeys(map3CIA) {
								for _, i := range sortedKeys(map3CIA) {
									for _, a := range sortedKeys(map3CIA) {
										out = append(out, prefix+"AV:"+av+"/AC:"+ac+"/PR:"+pr+"/UI:"+ui+"/S:"+s+"/C:"+c+"/I:"+i+"/A:"+a)
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

// firstBand is the FIRST qualitative rating scale, written out independently
// of BandFor: the score is rounded to one decimal, and v2 has no critical
// band, so its top range reads high.
func firstBand(score float64, v CVSSVersion) Severity {
	tenths := int(math.Round(score * 10))
	switch {
	case tenths == 0:
		return SeverityNone
	case tenths <= 39:
		return SeverityLow
	case tenths <= 69:
		return SeverityMedium
	case tenths <= 89 || v == CVSSv2:
		return SeverityHigh
	default:
		return SeverityCritical
	}
}

// checkScore asserts the invariants every successfully scored vector holds:
// the score is in [0,10] (also after BandFor's one-decimal rounding), and the
// band is known and matches the FIRST scale.
func checkScore(t *testing.T, vec string, score float64, v CVSSVersion) Severity {
	t.Helper()
	const tol = 1e-9
	if math.IsNaN(score) || score < -tol || score > 10+tol {
		t.Fatalf("BaseScore(%q) = %v, outside [0,10]", vec, score)
	}
	if r := math.Round(score*10) / 10; r < 0 || r > 10 {
		t.Fatalf("BaseScore(%q) rounds to %v, outside [0,10]", vec, r)
	}
	band := BandFor(score, v)
	if band == SeverityUnknown {
		t.Fatalf("BandFor(%v, %v) for %q is unknown", score, v, vec)
	}
	if want := firstBand(score, v); band != want {
		t.Fatalf("BandFor(%v, %v) for %q = %v, want %v", score, v, vec, band, want)
	}
	return band
}

// TestCVSSPropBandsAndMonotonic scores every vector of the v2 and v3 metric
// spaces and asserts the score range, the FIRST bands, that a higher score
// never gets a lower band, and that each version reaches every band it has.
func TestCVSSPropBandsAndMonotonic(t *testing.T) {
	versions := []struct {
		name    string
		vectors []string
		bands   []Severity
	}{
		{"v2", allCVSS2Vectors(), []Severity{SeverityNone, SeverityLow, SeverityMedium, SeverityHigh}},
		{"v3", allCVSS3Vectors(), []Severity{SeverityNone, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}},
	}
	for _, ver := range versions {
		t.Run(ver.name, func(t *testing.T) {
			var scored []scoredVector
			for _, vec := range ver.vectors {
				score, v, ok := BaseScore(vec)
				if !ok {
					t.Fatalf("BaseScore(%q) failed on a vector of known metric values", vec)
				}
				scored = append(scored, scoredVector{vec: vec, v: v, score: score, band: checkScore(t, vec, score, v)})
			}

			// Score order implies band order.
			sort.SliceStable(scored, func(i, j int) bool { return scored[i].score < scored[j].score })
			for i := 1; i < len(scored); i++ {
				prev, cur := scored[i-1], scored[i]
				if cur.band < prev.band {
					t.Fatalf("band fell as the score rose: %q %.4f -> %v, then %q %.4f -> %v",
						prev.vec, prev.score, prev.band, cur.vec, cur.score, cur.band)
				}
			}

			// No dead band.
			seen := map[Severity]int{}
			for _, s := range scored {
				seen[s.band]++
			}
			for _, b := range ver.bands {
				if seen[b] == 0 {
					t.Errorf("no %s vector scores in the %v band", ver.name, b)
				}
			}
			if ver.name == "v2" && seen[SeverityCritical] != 0 {
				t.Errorf("%d v2 vectors scored critical; v2 has no critical band", seen[SeverityCritical])
			}
		})
	}
}

// TestCVSSPropUnknownMetricFails replaces, one at a time, each metric the
// scorer validates with an unknown value and then drops it, and expects
// ok=false. The C/I/A impacts (both versions), PR and S (v3) are read
// leniently by cvss.go today (a missing or unknown value counts as none or
// unchanged), so they are not asserted here.
func TestCVSSPropUnknownMetricFails(t *testing.T) {
	cases := []struct {
		base    string
		metrics []string
	}{
		{"CVSS:2.0/AV:N/AC:L/Au:N/C:C/I:C/A:C", []string{"AV", "AC", "Au"}},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", []string{"AV", "AC", "UI"}},
		{"CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", []string{"AV", "AC", "UI"}},
	}
	for _, tc := range cases {
		if _, _, ok := BaseScore(tc.base); !ok {
			t.Fatalf("base vector %q does not score", tc.base)
		}
		for _, m := range tc.metrics {
			old := "/" + m + ":" + metric(tc.base, m)
			for _, replacement := range []string{"/" + m + ":Z", ""} {
				vec := strings.Replace(tc.base, old, replacement, 1)
				if vec == tc.base {
					t.Fatalf("metric %s not found in %q", m, tc.base)
				}
				if score, _, ok := BaseScore(vec); ok {
					t.Errorf("BaseScore(%q) = %v, ok; want ok=false for the bad %s", vec, score, m)
				}
			}
		}
	}
	// An unknown PR value is rejected in both scopes.
	for _, vec := range []string{
		"CVSS:3.1/AV:N/AC:L/PR:Z/UI:N/S:U/C:H/I:H/A:H",
		"CVSS:3.1/AV:N/AC:L/PR:Z/UI:N/S:C/C:H/I:H/A:H",
	} {
		if score, _, ok := BaseScore(vec); ok {
			t.Errorf("BaseScore(%q) = %v, ok; want ok=false for the unknown PR", vec, score)
		}
	}
	// Not a vector at all, and the unscored v4.0.
	for _, vec := range []string{"", "garbage", "CWSS:1.0/TI:H", "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"} {
		if _, _, ok := BaseScore(vec); ok {
			t.Errorf("BaseScore(%q) scored", vec)
		}
	}
}

// FuzzBaseScore asserts BaseScore never panics, and that anything it does
// score is in [0,10] with a known band matching the FIRST scale.
func FuzzBaseScore(f *testing.F) {
	f.Add("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
	f.Add("CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:L/A:N")
	f.Add("CVSS:2.0/AV:N/AC:L/Au:N/C:C/I:C/A:C")
	f.Add("AV:N/AC:L/Au:N/C:P/I:P/A:P")
	f.Add("CVSS:3.0/AV:P/AC:H/PR:H/UI:R/S:C/C:N/I:N/A:N")
	f.Add("")
	f.Fuzz(func(t *testing.T, vec string) {
		score, v, ok := BaseScore(vec)
		if !ok {
			return
		}
		checkScore(t, vec, score, v)
	})
}
