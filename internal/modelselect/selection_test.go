package modelselect

import (
	"reflect"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestSelectionSaveRestoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sel := Selection{Model: "claude-opus-4", Effort: "max"}
	if err := Save(sel); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !reflect.DeepEqual(got, sel) {
		t.Fatalf("round-trip mismatch: got=%+v want=%+v", got, sel)
	}
}

func TestSavePreservesLastMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := config.SaveState(config.State{Model: "old", Effort: "low", LastMode: "plan"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if err := Save(Selection{Model: "new", Effort: "high"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st, err := config.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.LastMode != "plan" {
		t.Fatalf("LastMode = %q, want preserved", st.LastMode)
	}
	if st.Model != "new" || st.Effort != "high" {
		t.Fatalf("state = %+v", st)
	}
}

func TestPickerSaveAsDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	p, err := NewPicker()
	if err != nil {
		t.Fatalf("NewPicker: %v", err)
	}
	p.Current = Selection{Model: "gpt-5", Effort: "high"}
	if err := p.SaveAsDefault(); err != nil {
		t.Fatalf("SaveAsDefault: %v", err)
	}
	got, err := Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got != p.Current {
		t.Fatalf("restored = %+v, want %+v", got, p.Current)
	}
}

func TestDefault(t *testing.T) {
	d := Default()
	if d.Model == "" || d.Effort == "" {
		t.Fatalf("Default = %+v", d)
	}
}
