package hooks

import "testing"

func TestParseHookFileValid(t *testing.T) {
	h, err := ParseHookFile([]byte(`{"name":"lint","event":"post_tool","command":"bin/lint"}`))
	if err != nil {
		t.Fatalf("ParseHookFile: %v", err)
	}
	if h.Name != "lint" || h.Event != "post_tool" || h.Command != "bin/lint" {
		t.Fatalf("hook = %+v", h)
	}
}

func TestParseHookFileMalformedJSON(t *testing.T) {
	if _, err := ParseHookFile([]byte(`{not json`)); err == nil {
		t.Fatalf("expected malformed json to be rejected")
	}
}

func TestValidateHookRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		h    Hook
	}{
		{"missing name", Hook{Event: "post_tool", Command: "bin/x"}},
		{"unknown event", Hook{Name: "x", Event: "anything", Command: "bin/x"}},
		{"empty command", Hook{Name: "x", Event: "post_tool", Command: ""}},
		{"absolute command", Hook{Name: "x", Event: "post_tool", Command: "/bin/rm"}},
		{"traversal", Hook{Name: "x", Event: "post_tool", Command: "../outside"}},
		{"shell metachar", Hook{Name: "x", Event: "post_tool", Command: "bin/rm;id"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateHook(tc.h); err == nil {
				t.Fatalf("expected hook to be rejected: %+v", tc.h)
			}
		})
	}
}

func TestValidateHookAcceptsRelativePath(t *testing.T) {
	h, err := ValidateHook(Hook{Name: "x", Event: "pre_edit", Command: "hooks/lint.sh"})
	if err != nil {
		t.Fatalf("ValidateHook: %v", err)
	}
	if h.Command != "hooks/lint.sh" {
		t.Fatalf("Command = %q", h.Command)
	}
}
