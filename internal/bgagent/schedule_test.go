package bgagent

import (
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", time.Minute},
		{"   ", time.Minute},
		{"50ms", 50 * time.Millisecond},
		{"2s", 2 * time.Second},
		{"2m", 2 * time.Minute},
		{"30", 30 * time.Minute},
		{" 30 ", 30 * time.Minute},
		{"0", time.Minute},   // zero/negative minutes -> default
		{"-5", time.Minute},  // negative -> default
		{"abc", time.Minute}, // unparseable -> default
		{"1.5h", 90 * time.Minute},
	}
	for _, c := range cases {
		if got := parseSchedule(c.in); got != c.want {
			t.Errorf("parseSchedule(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
