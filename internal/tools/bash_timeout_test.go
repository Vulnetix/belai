package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBashTimeoutArgument(t *testing.T) {
	b := &Bash{Root: t.TempDir()}
	cases := []struct {
		args map[string]any
		want time.Duration
	}{
		{map[string]any{}, BashDefaultTimeout},
		{map[string]any{"timeout": 5000}, 5 * time.Second},
		{map[string]any{"timeout": 10_000_000}, BashMaxTimeout},
	}
	for _, tc := range cases {
		got, err := b.effectiveTimeout(tc.args)
		if err != nil || got != tc.want {
			t.Fatalf("%v: timeout = %v, %v; want %v", tc.args, got, err, tc.want)
		}
	}
	if _, err := b.effectiveTimeout(map[string]any{"timeout": 0}); err == nil {
		t.Fatal("a non-positive timeout must be refused")
	}
	withDefault := &Bash{Timeout: 30 * time.Second}
	if got, _ := withDefault.effectiveTimeout(nil); got != 30*time.Second {
		t.Fatalf("tool default = %v", got)
	}
}

func TestBashTimeoutKillsAndKeepsOutput(t *testing.T) {
	b := &Bash{Root: t.TempDir()}
	res, err := b.Execute(context.Background(), map[string]any{"command": "echo started; sleep 5", "timeout": 200, "description": "sleep"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "started") || !strings.Contains(res.Content, "timed out after 200ms") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestBashAdvertisesTimeoutAndDescription(t *testing.T) {
	for _, ro := range []bool{false, true} {
		def := (&Bash{ReadOnly: ro}).Definition()
		for _, k := range []string{"command", "timeout", "description"} {
			if _, ok := def.Properties[k]; !ok {
				t.Fatalf("readOnly=%v: %q not declared", ro, k)
			}
		}
		if strings.Contains(def.Description, "30 seconds") {
			t.Fatalf("readOnly=%v: description still names the old fixed limit", ro)
		}
	}
}
