package tui

import "testing"

func TestParseLocalModelArgs(t *testing.T) {
	cases := []struct {
		in       string
		wantSub  string
		wantRepo string
		wantPort string
		wantQ    string
	}{
		{"", "", "", "", ""},
		{"status", "status", "", "", ""},
		{"launch org/model", "launch", "org/model", "", ""},
		{"launch org/model --port 1234 --quant Q5_K_M", "launch", "org/model", "1234", "Q5_K_M"},
		{"download org/model --quant Q4_K_M", "download", "org/model", "", "Q4_K_M"},
		{"stop --port 8080", "stop", "", "8080", ""},
	}
	for _, tc := range cases {
		sub, f := parseLocalModelArgs(tc.in)
		if sub != tc.wantSub || f.repo != tc.wantRepo || f.port != tc.wantPort || f.quant != tc.wantQ {
			t.Fatalf("parseLocalModelArgs(%q) = (%q, %+v), want (%q, repo=%q port=%q quant=%q)",
				tc.in, sub, f, tc.wantSub, tc.wantRepo, tc.wantPort, tc.wantQ)
		}
	}
}
