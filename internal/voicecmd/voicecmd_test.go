package voicecmd

import "testing"

func TestMatchKeyword(t *testing.T) {
	cases := []struct {
		in string
		k  Keyword
		n  int
		ok bool
	}{
		{"Stop.", Stop, 0, true},
		{"  please stop ", Stop, 0, true},
		{"Option 2", Option, 2, true},
		{"option two.", Option, 2, true},
		{"Number three", Option, 3, true},
		{"4", Option, 4, true},
		{"Submit", Submit, 0, true},
		{"Skip!", Skip, 0, true},
		{"Approved", Approve, 0, true},
		{"Approve always", ApproveAlways, 0, true},
		{"Deny", Deny, 0, true},
		{"stop the server", "", 0, false},
		{"please don't stop", "", 0, false},
		{"option 5", "", 0, false},
		{"option", "", 0, false},
		{"5", "", 0, false},
		{"", "", 0, false},
		{"I would deny that", "", 0, false},
	}
	for _, c := range cases {
		k, n, ok := MatchKeyword(c.in)
		if k != c.k || n != c.n || ok != c.ok {
			t.Errorf("MatchKeyword(%q) = %q,%d,%v; want %q,%d,%v", c.in, k, n, ok, c.k, c.n, c.ok)
		}
	}
}

func TestMatchWake(t *testing.T) {
	cases := []struct {
		in   string
		rest string
		ok   bool
	}{
		{"Hey, Belay.", "", true},
		{"Hey Belay, run the security review", "run the security review", true},
		{"hey belai plan mode", "plan mode", true},
		{"Hey Bellay start the crew", "start the crew", true},
		{"hay belay's goal mode", "goal mode", true},
		{"A belay, stop", "stop", true},
		{"Hey Bela", "", false},
		{"hey there belay", "", false},
		{"belay hey", "", false},
		{"they said hey belay", "", false},
		{"hey believe", "", false},
		{"hello world", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		rest, ok := MatchWake(c.in)
		if rest != c.rest || ok != c.ok {
			t.Errorf("MatchWake(%q) = %q,%v; want %q,%v", c.in, rest, ok, c.rest, c.ok)
		}
	}
}
