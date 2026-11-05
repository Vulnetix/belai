package config

// EditSettings governs how the Edit tool matches old_string (docs/edit.md).
type EditSettings struct {
	// Tolerant lets Edit apply a whitespace-only relaxed match (line endings,
	// trailing spaces and tabs, one consistent indentation shift) when it is
	// the only match, and report the nearest lines when nothing matches. nil
	// means on. Off, Edit matches byte for byte and returns its plain errors.
	Tolerant *bool `json:"tolerant,omitempty"`
}

// EditTolerant reports whether Edit may use its relaxed matching ladder.
// Default on.
func (s Settings) EditTolerant() bool {
	return s.Edit == nil || s.Edit.Tolerant == nil || *s.Edit.Tolerant
}

// mergeEdit folds in over cur. A user layer sets the switch either way. From
// the project layer only an explicit false takes effect: a repository may make
// Edit stricter for the people who open it, never looser.
func mergeEdit(cur, in *EditSettings, project bool) *EditSettings {
	if in == nil || in.Tolerant == nil {
		return cur
	}
	if project && *in.Tolerant {
		return cur
	}
	out := &EditSettings{}
	if cur != nil {
		*out = *cur
	}
	v := *in.Tolerant
	out.Tolerant = &v
	return out
}
