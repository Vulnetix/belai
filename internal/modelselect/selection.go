// Package modelselect persists the user's model and effort/thinking selection
// across sessions via config/state.
package modelselect

import "github.com/vulnetix/signet/internal/config"

// Selection is a model + effort/thinking level choice.
type Selection struct {
	Model  string
	Effort string
}

// Save persists the selection as the default, preserving other state fields
// (such as LastMode).
func Save(sel Selection) error {
	st, err := config.LoadState()
	if err != nil {
		return err
	}
	st.Model = sel.Model
	st.Effort = sel.Effort
	return config.SaveState(st)
}

// Restore returns the persisted selection. A missing state yields an empty
// Selection.
func Restore() (Selection, error) {
	st, err := config.LoadState()
	if err != nil {
		return Selection{}, err
	}
	return Selection{Model: st.Model, Effort: st.Effort}, nil
}

// Default returns the fallback selection when nothing is persisted.
func Default() Selection {
	return Selection{Model: "gpt-5", Effort: "medium"}
}

// Picker holds the current selection and can save it as the default.
type Picker struct {
	Current Selection
}

// NewPicker returns a Picker restored from persisted state.
func NewPicker() (Picker, error) {
	sel, err := Restore()
	if err != nil {
		return Picker{}, err
	}
	return Picker{Current: sel}, nil
}

// SaveAsDefault persists the current selection.
func (p Picker) SaveAsDefault() error {
	return Save(p.Current)
}
