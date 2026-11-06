package config

// PatchTolerance values (docs/patch-tool.md). Strict matches a hunk's lines
// byte for byte, whitespace also folds line endings, trailing space and one
// consistent indentation shift, and unicode adds typographic punctuation folded
// to ASCII.
const (
	PatchStrict     = "strict"
	PatchWhitespace = "whitespace"
	PatchUnicode    = "unicode"
)

// PatchTolerances lists the values in order from the strictest.
var PatchTolerances = []string{PatchStrict, PatchWhitespace, PatchUnicode}

// PatchToolEnabled reports whether the apply_patch tool is offered. Default on.
func (s Settings) PatchToolEnabled() bool {
	return s.PatchTool == nil || *s.PatchTool
}

// PatchToleranceValue is the effective patch_tolerance word: whitespace when
// unset, and strict for a value that is not one of the three, so a typo narrows
// the tool rather than widening it.
func (s Settings) PatchToleranceValue() string {
	switch s.PatchTolerance {
	case "":
		return PatchWhitespace
	case PatchStrict, PatchWhitespace, PatchUnicode:
		return s.PatchTolerance
	}
	return PatchStrict
}

// PatchToleranceRank orders the tolerance words from the strictest, and ranks
// an unknown word as the strictest.
func PatchToleranceRank(v string) int {
	for i, w := range PatchTolerances {
		if w == v {
			return i
		}
	}
	return 0
}

// mergePatchTool folds a layer's patch_tool over cur. A user layer sets it
// either way; the project layer can only switch the tool off.
func mergePatchTool(cur, in *bool, project bool) *bool {
	if in == nil || (project && *in) {
		return cur
	}
	v := *in
	return &v
}

// mergePatchTolerance folds a layer's patch_tolerance over cur. A user layer
// sets any of the three. The project layer can only move toward strict: its
// value applies when it is stricter than what the user layers resolved to (or
// than the default), and an unknown word counts as strict.
func mergePatchTolerance(cur, in string, project bool) string {
	if in == "" {
		return cur
	}
	if !project {
		return in
	}
	eff := Settings{PatchTolerance: cur}.PatchToleranceValue()
	if PatchToleranceRank(Settings{PatchTolerance: in}.PatchToleranceValue()) < PatchToleranceRank(eff) {
		return Settings{PatchTolerance: in}.PatchToleranceValue()
	}
	return cur
}
