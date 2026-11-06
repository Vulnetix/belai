package secretshape

import "testing"

func TestHolds(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"-----BEGIN RSA PRIVATE KEY-----\nabc", true},
		{"key = AKIAABCDEFGHIJKLMNOP", true},
		{"ghp_" + "0123456789abcdefghijklmnopqrstuvwxyz", true},
		{"plain source code with no token", false},
		{"AKIA short", false},
	} {
		if got := Holds([]byte(c.in)); got != c.want {
			t.Errorf("Holds(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
