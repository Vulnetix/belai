package config

import "fmt"

// SymbolIndexSettings configures the symbol index behind the Outline and
// Symbol tools (docs/symbol-map.md): the declarations of the working
// directory's eligible files, parsed by the harness and served only when the
// model asks.
//
// The index makes a host read and hold more of a repository, so the project
// layer, which a cloned repository controls, can only switch it off. Its
// numbers are the user's: a repository cannot make a host index or return
// more, nor change how often it re-reads.
type SymbolIndexSettings struct {
	// Enabled switches the index and both tools. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxFiles is how many eligible files one index covers. Default 2000,
	// 100 to 10000.
	MaxFiles *int `json:"max_files,omitempty"`
	// MaxResults is how many rows one Symbol call returns at most. Default 50,
	// 1 to 200.
	MaxResults *int `json:"max_results,omitempty"`
	// RefreshSecs is how long a built index is reused before a changed file is
	// looked for again. Default 60, 5 to 3600.
	RefreshSecs *int `json:"refresh_secs,omitempty"`
}

// Symbol index defaults and bounds.
const (
	DefaultSymbolIndexMaxFiles    = 2000
	DefaultSymbolIndexMaxResults  = 50
	DefaultSymbolIndexRefreshSecs = 60

	minSymbolIndexMaxFiles    = 100
	maxSymbolIndexMaxFiles    = 10000
	maxSymbolIndexMaxResults  = 200
	minSymbolIndexRefreshSecs = 5
	maxSymbolIndexRefreshSecs = 3600
)

// SymbolIndexEnabled reports whether the symbol index and its tools are on.
// Default on.
func (s Settings) SymbolIndexEnabled() bool {
	return s.SymbolIndex == nil || s.SymbolIndex.Enabled == nil || *s.SymbolIndex.Enabled
}

// SymbolIndexMaxFiles is the most files one index covers. An out-of-range value
// falls back to the default, so a bad value never reaches the walk.
func (s Settings) SymbolIndexMaxFiles() int {
	if c := s.SymbolIndex; c != nil && c.MaxFiles != nil && *c.MaxFiles >= minSymbolIndexMaxFiles && *c.MaxFiles <= maxSymbolIndexMaxFiles {
		return *c.MaxFiles
	}
	return DefaultSymbolIndexMaxFiles
}

// SymbolIndexMaxResults is the most rows one Symbol call returns.
func (s Settings) SymbolIndexMaxResults() int {
	if c := s.SymbolIndex; c != nil && c.MaxResults != nil && *c.MaxResults >= 1 && *c.MaxResults <= maxSymbolIndexMaxResults {
		return *c.MaxResults
	}
	return DefaultSymbolIndexMaxResults
}

// SymbolIndexRefreshSecs is how long a built index is reused.
func (s Settings) SymbolIndexRefreshSecs() int {
	if c := s.SymbolIndex; c != nil && c.RefreshSecs != nil && *c.RefreshSecs >= minSymbolIndexRefreshSecs && *c.RefreshSecs <= maxSymbolIndexRefreshSecs {
		return *c.RefreshSecs
	}
	return DefaultSymbolIndexRefreshSecs
}

// ValidateSymbolIndex rejects a symbol_index key outside its range, naming the
// key. A repository's value never gets here: mergeSymbolIndex ignores it.
func ValidateSymbolIndex(s Settings) error {
	c := s.SymbolIndex
	if c == nil {
		return nil
	}
	if p := c.MaxFiles; p != nil && (*p < minSymbolIndexMaxFiles || *p > maxSymbolIndexMaxFiles) {
		return fmt.Errorf("symbol_index.max_files %d must be between %d and %d", *p, minSymbolIndexMaxFiles, maxSymbolIndexMaxFiles)
	}
	if p := c.MaxResults; p != nil && (*p < 1 || *p > maxSymbolIndexMaxResults) {
		return fmt.Errorf("symbol_index.max_results %d must be between 1 and %d", *p, maxSymbolIndexMaxResults)
	}
	if p := c.RefreshSecs; p != nil && (*p < minSymbolIndexRefreshSecs || *p > maxSymbolIndexRefreshSecs) {
		return fmt.Errorf("symbol_index.refresh_secs %d must be between %d and %d", *p, minSymbolIndexRefreshSecs, maxSymbolIndexRefreshSecs)
	}
	return nil
}

// mergeSymbolIndex folds in over cur. A user layer sets any key in range. The
// project layer only switches the index off; every number it carries, and an
// enabled of true, is ignored. An out-of-range value from any layer is dropped.
func mergeSymbolIndex(cur, in *SymbolIndexSettings, project bool) *SymbolIndexSettings {
	if in == nil {
		return cur
	}
	out := &SymbolIndexSettings{}
	if cur != nil {
		*out = *cur
	}
	if in.Enabled != nil && !(project && *in.Enabled) {
		v := *in.Enabled
		out.Enabled = &v
	}
	if project {
		if cur == nil && out.Enabled == nil {
			return nil
		}
		return out
	}
	if p := in.MaxFiles; p != nil && *p >= minSymbolIndexMaxFiles && *p <= maxSymbolIndexMaxFiles {
		v := *p
		out.MaxFiles = &v
	}
	if p := in.MaxResults; p != nil && *p >= 1 && *p <= maxSymbolIndexMaxResults {
		v := *p
		out.MaxResults = &v
	}
	if p := in.RefreshSecs; p != nil && *p >= minSymbolIndexRefreshSecs && *p <= maxSymbolIndexRefreshSecs {
		v := *p
		out.RefreshSecs = &v
	}
	return out
}

// symbolIndexIgnored names the project keys mergeSymbolIndex would not apply,
// for the note Resolve prints.
func symbolIndexIgnored(proj *SymbolIndexSettings) []string {
	if proj == nil {
		return nil
	}
	var out []string
	if proj.Enabled != nil && *proj.Enabled {
		out = append(out, "symbol_index.enabled")
	}
	if proj.MaxFiles != nil {
		out = append(out, "symbol_index.max_files")
	}
	if proj.MaxResults != nil {
		out = append(out, "symbol_index.max_results")
	}
	if proj.RefreshSecs != nil {
		out = append(out, "symbol_index.refresh_secs")
	}
	return out
}
