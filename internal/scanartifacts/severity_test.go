package scanartifacts

import "testing"

func TestSeverityString(t *testing.T) {
	cases := []struct {
		s    Severity
		want string
	}{
		{SeverityUnknown, "unknown"},
		{SeverityNone, "none"},
		{SeverityInfo, "info"},
		{SeverityLow, "low"},
		{SeverityMedium, "medium"},
		{SeverityHigh, "high"},
		{SeverityCritical, "critical"},
		{Severity(99), "unknown"}, // out-of-range bucket
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("Severity(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestSeverityFromWord(t *testing.T) {
	cases := []struct {
		in   string
		want Severity
	}{
		{"none", SeverityNone},
		{"NONE", SeverityNone},
		{"  none  ", SeverityNone},
		{"negligible", SeverityNone},
		{"info", SeverityInfo},
		{"informational", SeverityInfo},
		{"information", SeverityInfo},
		{"low", SeverityLow},
		{"medium", SeverityMedium},
		{"moderate", SeverityMedium},
		{"high", SeverityHigh},
		{"critical", SeverityCritical},
		{"severe", SeverityCritical},
		{"bogus", SeverityUnknown},
		{"", SeverityUnknown},
	}
	for _, tc := range cases {
		if got := SeverityFromWord(tc.in); got != tc.want {
			t.Errorf("SeverityFromWord(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCountsAddAndTotal(t *testing.T) {
	var c Counts
	c.Add(SeverityCritical, 1)
	c.Add(SeverityHigh, 2)
	c.Add(SeverityMedium, 3)
	c.Add(SeverityLow, 4)
	c.Add(SeverityInfo, 5)
	c.Add(SeverityNone, 6)
	c.Add(SeverityUnknown, 7)
	// Out-of-range severity falls into the unknown bucket.
	c.Add(Severity(99), 1)

	if c.Critical != 1 || c.High != 2 || c.Medium != 3 || c.Low != 4 ||
		c.Info != 5 || c.None != 6 || c.Unknown != 8 {
		t.Fatalf("unexpected buckets: %+v", c)
	}
	if c.Total() != 29 {
		t.Fatalf("Total() = %d, want 29", c.Total())
	}
}

func TestCountsMax(t *testing.T) {
	cases := []struct {
		c    Counts
		want Severity
	}{
		{Counts{}, SeverityUnknown},
		{Counts{Unknown: 1}, SeverityUnknown},
		{Counts{None: 1}, SeverityNone},
		{Counts{Info: 1, Low: 2}, SeverityLow},
		{Counts{Low: 1, Medium: 2}, SeverityMedium},
		{Counts{Medium: 1, High: 2}, SeverityHigh},
		{Counts{High: 1, Critical: 2}, SeverityCritical},
	}
	for _, tc := range cases {
		if got := tc.c.Max(); got != tc.want {
			t.Errorf("Counts%+v.Max() = %v, want %v", tc.c, got, tc.want)
		}
	}
}

func TestCountsMerge(t *testing.T) {
	a := Counts{Critical: 1, High: 2, Low: 3}
	b := Counts{High: 4, Medium: 5, Info: 6}
	got := a.Merge(b)
	if got.Critical != 1 || got.High != 6 || got.Medium != 5 || got.Low != 3 || got.Info != 6 {
		t.Fatalf("Merge = %+v", got)
	}
	// Inputs must not be mutated.
	if a.High != 2 || b.High != 4 {
		t.Fatalf("Merge mutated inputs: a=%+v b=%+v", a, b)
	}
}

func TestCountsFormat(t *testing.T) {
	c := Counts{Critical: 1, High: 2, Medium: 3, Low: 4, Unknown: 5}
	want := "1 critical · 2 high · 3 medium · 4 low · 5 unknown"
	if got := c.Format(); got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

func TestSummaryHasFindingsAndFormat(t *testing.T) {
	var s Summary
	if s.HasFindings() {
		t.Fatal("empty summary should have no findings")
	}
	s.Union = Counts{High: 1}
	if !s.HasFindings() {
		t.Fatal("summary with a high should have findings")
	}
	if got := s.Format(); got != "0 critical · 1 high · 0 medium · 0 low · 0 unknown" {
		t.Fatalf("Format() = %q", got)
	}
}

func TestUniqueKeys(t *testing.T) {
	items := []FindingKey{
		{Key: "a", Severity: SeverityLow},
		{Key: "b", Severity: SeverityHigh},
		{Key: "a", Severity: SeverityCritical}, // duplicate: first-seen severity wins
		{Key: "c", Severity: SeverityMedium},
		{Key: "b", Severity: SeverityInfo},
	}
	got := UniqueKeys(items)
	want := []FindingKey{
		{Key: "a", Severity: SeverityLow},
		{Key: "b", Severity: SeverityHigh},
		{Key: "c", Severity: SeverityMedium},
	}
	if len(got) != len(want) {
		t.Fatalf("UniqueKeys = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UniqueKeys[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
