package scanartifacts

import (
	"math"
	"testing"
)

func TestDetectVector(t *testing.T) {
	cases := []struct {
		vec string
		v   CVSSVersion
		ok  bool
	}{
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", CVSSv31, true},
		{"CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", CVSSv30, true},
		{"CVSS:2.0/AV:N/AC:L/Au:N/C:C/I:C/A:C", CVSSv2, true},
		{"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H", CVSSv40, true},
		{"CWSS:1.0/TI:H/AP:A", CVSSUnknown, false},
		{"garbage", CVSSUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.vec, func(t *testing.T) {
			v, ok := DetectVector(tc.vec)
			if ok != tc.ok || v != tc.v {
				t.Fatalf("DetectVector(%q) = (%v, %v), want (%v, %v)", tc.vec, v, ok, tc.v, tc.ok)
			}
		})
	}
}

func TestCVSS3FirstExamples(t *testing.T) {
	// FIRST CVSS v3.1 published example vectors and scores.
	cases := []struct {
		vec   string
		score float64
		sev   Severity
	}{
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", 9.8, SeverityCritical},
		{"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:L/A:N", 5.3, SeverityMedium},
	}
	for _, tc := range cases {
		t.Run(tc.vec, func(t *testing.T) {
			score, v, ok := BaseScore(tc.vec)
			if !ok {
				t.Fatal("parse failed")
			}
			if math.Abs(score-tc.score) > 0.05 {
				t.Fatalf("score = %.2f, want %.2f", score, tc.score)
			}
			if sev := BandFor(score, v); sev != tc.sev {
				t.Fatalf("severity = %v, want %v", sev, tc.sev)
			}
		})
	}
}

func TestCVSS2Example(t *testing.T) {
	vec := "CVSS:2.0/AV:N/AC:L/Au:N/C:C/I:C/A:C"
	score, v, ok := BaseScore(vec)
	if !ok {
		t.Fatal("parse failed")
	}
	if sev := BandFor(score, v); sev != SeverityHigh {
		t.Fatalf("v2 10.0 should map to high, got %v", sev)
	}
}

func TestBandBoundaries(t *testing.T) {
	tests := []struct {
		score float64
		v     CVSSVersion
		want  Severity
	}{
		{0.0, CVSSv31, SeverityNone},
		{0.1, CVSSv31, SeverityLow},
		{3.9, CVSSv31, SeverityLow},
		{4.0, CVSSv31, SeverityMedium},
		{6.9, CVSSv31, SeverityMedium},
		{7.0, CVSSv31, SeverityHigh},
		{8.9, CVSSv31, SeverityHigh},
		{9.0, CVSSv31, SeverityCritical},
		{10.0, CVSSv31, SeverityCritical},
		{9.0, CVSSv2, SeverityHigh},
	}
	for _, tc := range tests {
		t.Run(tc.want.String(), func(t *testing.T) {
			if got := BandFor(tc.score, tc.v); got != tc.want {
				t.Fatalf("BandFor(%.1f, %v) = %v, want %v", tc.score, tc.v, got, tc.want)
			}
		})
	}
}

func TestCVSS40WithoutScoreDegrades(t *testing.T) {
	vec := "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H"
	_, _, ok := BaseScore(vec)
	if ok {
		t.Fatal("v4.0 should not compute a score")
	}
}
