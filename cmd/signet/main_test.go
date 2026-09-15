package main

import (
	"os"
	"testing"
)

func TestInteractiveDecision(t *testing.T) {
	cases := []struct {
		name      string
		stdoutTTY bool
		stdinTTY  bool
		ci        string
		noTUI     string
		want      bool
	}{
		{"both tty", true, true, "", "", true},
		{"stdout only", true, false, "", "", false},
		{"stdin only", false, true, "", "", false},
		{"neither tty", false, false, "", "", false},
		{"ci set", true, true, "true", "", false},
		{"signet_no_tui set", true, true, "", "1", false},
		{"both tty despite empty ci", true, true, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", tc.ci)
			t.Setenv("SIGNET_NO_TUI", tc.noTUI)
			got := interactive(tc.stdoutTTY, tc.stdinTTY, os.Getenv)
			if got != tc.want {
				t.Fatalf("interactive = %v, want %v", got, tc.want)
			}
		})
	}
}
