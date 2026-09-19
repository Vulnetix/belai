package commands

import "testing"

func TestParseInvocation(t *testing.T) {
	cases := []struct {
		in   string
		want Action
	}{
		{"", ActionRun},
		{"run", ActionRun},
		{"configure", ActionConfigure},
		{"list", ActionList},
		{"status", ActionStatus},
		{"help", ActionHelp},
		{"RUN extra", ActionRun},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			inv, err := ParseInvocation(tc.in)
			if err != nil {
				t.Fatalf("ParseInvocation: %v", err)
			}
			if inv.Action != tc.want {
				t.Fatalf("action = %d, want %d", inv.Action, tc.want)
			}
		})
	}
}

func TestParseInvocationUnknown(t *testing.T) {
	if _, err := ParseInvocation("frobnicate"); err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
}
