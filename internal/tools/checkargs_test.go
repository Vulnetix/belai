package tools

import (
	"strings"
	"testing"
)

func TestCheckArgs(t *testing.T) {
	read := (&Read{}).Definition()
	grep := (&Grep{}).Definition()
	bash := (&Bash{}).Definition()

	for _, tc := range []struct {
		name string
		def  Definition
		args map[string]any
		bad  string
	}{
		{"declared", read, map[string]any{"file_path": "a", "offset": 3, "limit": 2}, ""},
		{"alias", read, map[string]any{"path": "a"}, ""},
		{"no args", grep, map[string]any{}, ""},
		{"bash advisory", bash, map[string]any{"command": "ls", "description": "list", "timeout": 5000}, ""},
		{"grep trained flags", grep, map[string]any{"pattern": "x", "-i": true, "glob": "*.go"}, `"-i", "glob"`},
		{"bash background", bash, map[string]any{"command": "ls", "run_in_background": true}, `"run_in_background"`},
		{"read pages", read, map[string]any{"file_path": "a", "pages": "1-2"}, `"pages"`},
	} {
		err := CheckArgs(tc.def, tc.args)
		if tc.bad == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.bad) || !strings.Contains(err.Error(), "accepted arguments:") {
			t.Fatalf("%s: err = %v, want it to name %s", tc.name, err, tc.bad)
		}
	}
}

// No tool in the default registry may be rejected for the keys it declares
// itself, and the alias must not leak onto a tool that declares neither name.
func TestCheckArgsAliasNeedsDeclaredPath(t *testing.T) {
	ws := (&WebSearch{}).Definition()
	if err := CheckArgs(ws, map[string]any{"query": "q", "path": "x"}); err == nil {
		t.Fatal("path alias accepted on a tool without file_path or path")
	}
}
